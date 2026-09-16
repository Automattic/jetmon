
/**
 * Standalone check for Web Bot Auth (RFC 9421) request signing in the jetmon addon.
 *
 * Run: node test/bot-auth.js   (after: node-gyp rebuild && cp build/Release/jetmon.node lib/)
 */

const assert = require( 'assert' );
const crypto = require( 'crypto' );
const http   = require( 'http' );
const watcher = require( '../lib/jetmon.node' );

const KEY_ID        = 'test-key-1';
const DIRECTORY_URL = 'https://example.com/.well-known/http-message-signatures-directory';

const { privateKey, publicKey } = crypto.generateKeyPairSync( 'ed25519' );
const keyPem = privateKey.export( { type: 'pkcs8', format: 'pem' } );

// Rebuilds the RFC 9421 signature base from the received request and verifies it.
function verifyRequest( req ) {
	const sigInput  = req.headers['signature-input'];
	const sigHeader = req.headers['signature'];

	assert.ok( sigInput, 'Signature-Input header present' );
	assert.ok( sigHeader, 'Signature header present' );
	assert.ok( req.headers['signature-agent'], 'Signature-Agent header present' );

	const m = sigInput.match( /^sig1=(\(.*\).*)$/ );
	assert.ok( m, 'Signature-Input has sig1 label' );
	const params = m[1];

	assert.ok( params.includes( 'keyid="' + KEY_ID + '"' ), 'keyid matches' );
	assert.ok( params.includes( 'alg="ed25519"' ), 'alg is ed25519' );
	assert.ok( params.includes( 'tag="web-bot-auth"' ), 'tag is web-bot-auth' );

	const created = parseInt( params.match( /;created=(\d+)/ )[1], 10 );
	const expires = parseInt( params.match( /;expires=(\d+)/ )[1], 10 );
	assert.strictEqual( expires - created, 300, 'signature validity window is 300s' );
	assert.ok( Math.abs( Date.now() / 1000 - created ) < 60, 'created timestamp is sane' );

	const listMatch = m[1].match( /^\(([^)]*)\)/ );
	assert.ok( listMatch, 'Signature-Input has a component list' );
	const components = listMatch[1].split( ' ' ).map( s => s.replace( /"/g, '' ) );

	const qPos  = req.url.indexOf( '?' );
	const values = {
		'@method':         'head',
		'@authority':      req.headers.host,
		'@path':           qPos === -1 ? req.url : req.url.slice( 0, qPos ),
		'@query':          qPos === -1 ? undefined : req.url.slice( qPos ),
		'signature-agent': req.headers['signature-agent'],
	};

	let base = '';
	for ( const name of components ) {
		assert.notStrictEqual( values[name], undefined, 'component has a value: ' + name );
		base += '"' + name + '": ' + values[name] + '\n';
	}
	base += '"@signature-params": ' + params;

	const sig = Buffer.from( sigHeader.match( /^sig1=:([^:]+):$/ )[1], 'base64' );
	assert.strictEqual( sig.length, 64, 'Ed25519 signature is 64 bytes' );

	assert.ok(
		crypto.verify( null, Buffer.from( base ), publicKey, sig ),
		'signature verifies against public key'
	);
	assert.strictEqual( req.headers['signature-agent'], DIRECTORY_URL, 'Signature-Agent matches' );
}

function httpCheck( url ) {
	return new Promise( ( resolve, reject ) => {
		watcher.http_check( url, 80, 0, ( index, rtt, http_code, error_code ) => {
			if ( 200 !== http_code )
				return reject( new Error( 'check failed: http_code=' + http_code + ' error_code=' + error_code ) );
			resolve();
		} );
	} );
}

const requests = [];
let serverPort = 0;
const server = http.createServer( ( req, res ) => {
	requests.push( req );
	if ( '/redirect' === req.url ) {
		res.writeHead( 301, { 'Location': 'http://127.0.0.1:' + serverPort + '/final' } );
		res.end();
		return;
	}
	res.writeHead( 200, { 'Content-Type': 'text/plain' } );
	res.end();
} );

server.listen( 0, '127.0.0.1', async () => {
	const port = server.address().port;
	serverPort = port;
	try {
		// 1. Unsigned by default: no signing configured yet.
		await httpCheck( 'http://127.0.0.1:' + port + '/unsigned' );
		assert.strictEqual( requests[0].headers['signature'], undefined, 'no Signature header when unconfigured' );
		assert.strictEqual( requests[0].headers['signature-input'], undefined, 'no Signature-Input header when unconfigured' );

		// 2. Bad key material is rejected.
		assert.strictEqual( watcher.configure_signing( 'not a pem', KEY_ID, DIRECTORY_URL ), false, 'rejects invalid key' );

		// 3. Valid key: request is signed and verifies, including @query coverage.
		assert.strictEqual( watcher.configure_signing( keyPem, KEY_ID, DIRECTORY_URL ), true, 'accepts Ed25519 key' );
		await httpCheck( 'http://127.0.0.1:' + port + '/signed/path?x=1&y=2' );
		verifyRequest( requests[1] );
		assert.strictEqual( requests[1].url, '/signed/path?x=1&y=2', 'request target unchanged' );

		// 4. Redirect: each hop is re-signed with its own @path.
		await httpCheck( 'http://127.0.0.1:' + port + '/redirect' );
		assert.strictEqual( requests.length, 4, 'redirect followed to /final' );
		assert.strictEqual( requests[2].url, '/redirect', 'first hop is /redirect' );
		assert.strictEqual( requests[3].url, '/final', 'second hop is /final' );
		verifyRequest( requests[2] );
		verifyRequest( requests[3] );

		console.log( 'bot-auth signing test: OK' );
		server.close();
		process.exit( 0 );
	} catch ( err ) {
		console.error( 'bot-auth signing test: FAILED' );
		console.error( err );
		server.close();
		process.exit( 1 );
	}
} );
