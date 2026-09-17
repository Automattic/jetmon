
/**
 * Live interop check for Web Bot Auth signing against Cloudflare's deployed
 * verification, using the test endpoint from Cloudflare's docs:
 *
 *   https://crawltest.com/cdn-cgi/web-bot-auth
 *   (200 = verified, 401 = well-formed but unknown key, 400 = malformed)
 *
 * A fresh throwaway key is generated on each run, so the expected result for
 * the signed request is 401: proof that Cloudflare accepts our wire format.
 * A 400 means our request is malformed.
 *
 * Requires network access. Run: node test/bot-auth-crawltest.js
 * (after: node-gyp rebuild && cp build/Release/jetmon.node lib/)
 */

const assert = require( 'assert' );
const crypto = require( 'crypto' );
const watcher = require( '../lib/jetmon.node' );

const DIRECTORY_URL = 'https://example.com/.well-known/http-message-signatures-directory';
const TEST_URL      = 'https://crawltest.com/cdn-cgi/web-bot-auth';

const { privateKey, publicKey } = crypto.generateKeyPairSync( 'ed25519' );
const keyPem = privateKey.export( { type: 'pkcs8', format: 'pem' } );

// RFC 7638/8037 JWK thumbprint as keyid (see config/config.readme).
const jwk = publicKey.export( { format: 'jwk' } );
const KEY_ID = crypto.createHash( 'sha256' )
	.update( JSON.stringify( { crv: jwk.crv, kty: jwk.kty, x: jwk.x } ) )
	.digest( 'base64url' );

function httpCheck( url ) {
	return new Promise( ( resolve, reject ) => {
		watcher.http_check( url, 443, 0, ( index, rtt, http_code, error_code ) => {
			if ( 0 !== error_code )
				return reject( new Error( 'check failed: http_code=' + http_code + ' error_code=' + error_code + ' (network or endpoint problem?)' ) );
			resolve( http_code );
		} );
	} );
}

( async () => {
	try {
		assert.strictEqual( watcher.configure_signing( keyPem, KEY_ID, DIRECTORY_URL ), true, 'accepts Ed25519 key' );

		const signedCode = await httpCheck( TEST_URL );
		console.log( 'signed   ->', signedCode );
		assert.ok( 401 === signedCode || 200 === signedCode,
			'signed request is well-formed (401 unknown key / 200 verified), got ' + signedCode );

		watcher.clear_signing();
		const unsignedCode = await httpCheck( TEST_URL );
		console.log( 'unsigned ->', unsignedCode );
		assert.strictEqual( unsignedCode, 400, 'unsigned request is rejected as malformed' );

		console.log( 'bot-auth crawltest interop check: OK' );
		process.exit( 0 );
	} catch ( err ) {
		console.error( 'bot-auth crawltest interop check: FAILED' );
		console.error( err );
		process.exit( 1 );
	}
} )();
