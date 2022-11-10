
process.title = 'jetmon-server';

var _https = require( 'https'   );
var _url   = require( 'url'     );
var _fs    = require( 'fs'      );
var config = require( './config' );
config.load();

var o_log4js   = require( 'log4js' );

o_log4js.configure( {
  appenders: [ {
		'type'      : 'file',
		'filename'  : 'logs/jetmon.log',
		'maxLogSize': 52428800,
		'backups'   : 30,
		'category'  : 'flog',
		'levels'    : 'DEBUG',
		}
	]
});
o_log4js.PatternLayout = '%d{HH:mm:ss,SSS} p m';
var logger = o_log4js.getLogger( 'flog' );

const NETWORK_TIMEOUT_MS = 30000;

const PortNum = 7800;

const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

const HOST_OFFLINE = 0;
const HOST_ONLINE  = 1;

const ERROR   = -1;
const SUCCESS = 1;

var veriflierArray = config.get( 'VERIFIERS' );
var ssl_key        = _fs.readFileSync( 'certs/jetmon.key' );
var ssl_cert       = _fs.readFileSync( 'certs/jetmon.crt' );

var https_server = function() {

	var routes = {
		'/' : function( request, response ) {
			response.writeHead( 404, { 'Content-Type': 'text/html' } );
			response.write( 'Unsupported call\n' );
			response.end();
		},

		'/put/host-status' : function( request, response ) {
			if ( 'POST' === request.method ) {
				request.setEncoding( 'utf8' );
				var s_data = '';
				request.on( 'data', function( response_data ) {
					s_data += response_data;
				});
				request.on( 'end', function() {
					var reply_data = {};
					try {
						req = JSON.parse( s_data );
						if ( undefined === req.auth_token || undefined === req.checks || 0 == req.checks.length ) {
							response.writeHead( 404, { 'Content-Type': 'application/json' } );
							response.write( '{"response":' + ERROR + '}' );
							response.end();
							logger.error( 'invalid JSON POST data provided by ' +
										request.connection.remoteAddress );
							return;
						}

						var veriflier = false;
						for ( var count in veriflierArray ) {
							if ( req.auth_token == veriflierArray[ count ].auth_token ) {
								veriflier = true;
								req.veriflier_host = veriflierArray[ count ].host;
								break;
							}
						}

						if ( false === veriflier ) {
							response.writeHead( 503, { 'Content-Type': 'application/json' } );
							response.write( '{"response":' + ERROR + '}' );
							response.end();
							logger.error( 'invalid auth_code provided ' + req.auth_code );
							return;
						}

						response.writeHead( 200, { 'Content-Type': 'application/json' } );
						response.write( '{"response":' + SUCCESS + '}' );
						response.end();

						process.send( { msgtype: 'host_status_array', payload: req } );
					}
					catch ( Exception ) {
						logger.error( 'error parsing status reply data: ' + Exception.toString() );
					}
				});
			} else {
				var _get = _url.parse( request.url, true ).query;
				if ( ( undefined == _get['d'] ) && ( "" == _get['d'] ) ) {
					response.writeHead( 503, { 'Content-Type': 'application/json' } );
					response.write( '{"response":' + ERROR + '}' );
					response.end();
					logger.error( 'malformed request from server:' + _get );
					return;
				}

				var req = JSON.parse( _get['d'] );
				if ( undefined === req.auth_token || undefined === req.blog_id || undefined === req.status ) {
					response.writeHead( 404, { 'Content-Type': 'application/json' } );
					response.write( '{"response":' + ERROR + '}' );
					response.end();
					logger.error( 'invalid JSON data provided ' + _get['d'] );
					return;
				}

				var veriflier = false;
				for ( var count in veriflierArray ) {
					if ( req.auth_token == veriflierArray[ count ].auth_token ) {
						veriflier = true;
						req.veriflier_host = veriflierArray[ count ].host;
						break;
					}
				}

				if ( false === veriflier ) {
					response.writeHead( 503, { 'Content-Type': 'application/json' } );
					response.write( '{"response":' + ERROR + '}' );
					response.end();
					logger.error( 'invalid auth_code provided ' + req.auth_code );
					return;
				}

				response.writeHead( 200, { 'Content-Type': 'application/json' } );
				response.write( '{"response":' + SUCCESS + '}' );
				response.end();

				process.send( { msgtype: 'host_status', payload: req } );
			}
		},

		'/get/status' : function( request, response ) {
			logger.error( 'status confirmation requested' );
			response.writeHead( 200, { 'Content-Type': 'text/plain' } );
			response.write( 'OK' );
			response.end();
		}
	};

	var ssl_options = {
		key:      ssl_key,
		cert:     ssl_cert,
	};

	var request_handler = function( request, response ) {
		var arr_req = request.url.toString().split( '?' );
		if ( arr_req instanceof Array ) {
			if( undefined === routes[ arr_req[0] ] ) {
				response.writeHead( 404, { 'Content-Type': 'text/plain' } );
				response.write( 'not found\n' );
				response.end();
			} else {
				routes[ arr_req[0] ].call( this, request, response );
			}
		} else {
			response.writeHead( 404, { 'Content-Type': 'text/plain' } );
			response.write( 'Unsupported call\n' );
			response.end();
			logger.error( 'unsupported call: ' + request.url.toString() );
		}
	};

	var close_handler = function() {
		logger.error( process.pid + ': HTTPS server has been shutdown.' );
	};

	var error_handler = function( err ) {
		logger.error( process.pid + ': HTTPS error encountered: ' + err );
	};

	var _server = _https.createServer( ssl_options ).
					 addListener( 'request', request_handler )
					.addListener( 'close', close_handler )
					.addListener( 'error', error_handler )
					.listen( PortNum );
};

new https_server();

