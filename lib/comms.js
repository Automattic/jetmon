
var _https = require( 'https'   );
var _url   = require( 'url'     );
var _fs    = require( 'fs'      );
var _wpcom = require( './wpcom' );

const NETWORK_TIMEOUT_MS = 20000;

const PortNum = 7800;

const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

const HOST_OFFLINE = 0;
const HOST_ONLINE  = 1;

const ERROR   = -1;
const SUCCESS = 1;

// This determines how many peers have to confirm that the
// site is down before a notification email is sent
const PEER_OFFLINE_LIMIT = 2;

var veriflierArray = global.config.get( 'VERIFIERS' );
var ssl_key        = _fs.readFileSync('certs/jetmon.key');
var ssl_cert       = _fs.readFileSync('certs/jetmon.crt');

var https_server = function() {

	var routes = {
		'/' : function( request, response ) {
			response.writeHead( 404, { 'Content-Type': 'text/html' } );
			response.write( 'Unsupported call\n' );
			response.end();
		},

		'/put/host-status' : function( request, response ) {
			var _get = _url.parse( request.url, true ).query;
			if ( ( undefined == _get['d'] ) && ( "" == _get['d'] ) ) {
				response.writeHead( 503, { 'Content-Type': 'application/json' } );
				response.write( '{"response":' + ERROR + '}' );
				response.end();
				global.global.logger.error( 'malformed request from server:' + _get );
				return;
			}

			var req = JSON.parse( _get['d'] );

			if ( undefined === req.m_url || undefined === req.auth_token ||
				undefined === req.blog_id || undefined === req.status ) {
				response.writeHead( 404, { 'Content-Type': 'application/json' } );
				response.write( '{"response":' + ERROR + '}' );
				response.end();
				global.global.logger.error( 'invalid JSON data provided ' + _get['d'] );
				return;
			}

			var veriflier = false;
			for ( var count in veriflierArray ) {
				if ( req.auth_token == veriflierArray[ count ].auth_token ) {
					veriflier = veriflierArray[ count ];
					break;
				}
			}

			if ( ! veriflier ) {
				response.writeHead( 503, { 'Content-Type': 'application/json' } );
				response.write( '{"response":' + ERROR + '}' );
				response.end();
				global.global.logger.error( 'invalid auth_code provided ' + req.auth_code );
				return;
			}

			response.writeHead( 200, { 'Content-Type': 'application/json' } );
			response.write( '{"response":' + SUCCESS + '}' );
			response.end();

			for( var loop = queuedRetries.length - 1; loop >= 0; loop-- ) {
				if ( queuedRetries[ loop ].blog_id == req.blog_id ) {
					if ( HOST_OFFLINE == req.status ) {
						queuedRetries[ loop ].offline_confirms++;
						if ( queuedRetries[ loop ].offline_confirms >= PEER_OFFLINE_LIMIT ) {
							queuedRetries[ loop ].site_status = SITE_DOWN;
							_wpcom.notifyStatusChange( queuedRetries[ loop ],
													function( reply ) {
														if ( reply.success ) {
															global.logger.trace( 'posted successfully' );
														} else {
															global.logger.error( 'error posting status change, retrying...' );
															_wpcom.notifyStatusChange( queuedRetries[ loop ],
																					function( reply ) {
																						if ( reply.success )
																							global.logger.trace( 'posted successfully' );
																						else
																							global.logger.error( 'error posting status change.' );
															});
														}
							});
						}
					}
					queuedRetries[ loop ].requests_outstanding--;
					queuedRetries[ loop ].last_activity = new Date().valueOf();
					break;
				}
			}
		},

		'/get/status' : function( request, response ) {
			global.global.logger.error( 'status confirmation requested' );
			response.writeHead( 200, { 'Content-Type': 'text/plain' } );
			response.write( 'OK' );
			response.end();
		}
	}

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
			global.global.logger.error( 'unsupported call: ' + request.url.toString() );
		}
	};

	var close_handler = function() {
		global.global.logger.error( process.pid + ': HTTPS server has been shutdown.' );
	};

	var error_handler = function( err ) {
		global.global.logger.error( process.pid + ': HTTPS error encountered: ' + err );
	}

	var _server = _https.createServer( ssl_options ).
					 addListener( 'request', request_handler )
					.addListener( 'close', close_handler )
					.addListener( 'error', error_handler )
					.listen( PortNum );
};

// Start the HTTPS server
new https_server();

var client = {
	get_remote_status: function( veriflier_server, blog_id, s_server, call_back ) {
		var o_request         = new Object();
		o_request.auth_token  = global.config.get( 'AUTH_TOKEN' );
		o_request.blog_id     = blog_id;
		o_request.monitor_url = s_server;
		var request_str       = JSON.stringify( o_request );

		var options = {
			hostname: veriflier_server.host,
			port:     veriflier_server.port,
			key:      ssl_key,
			cert:     ssl_cert,
			path:     '/get/host-status?d=' + request_str,
			method:   'GET',
			rejectUnauthorized: false,
		};

		var response_handler = function( res ) {
			res.setEncoding( 'utf8' );
			var s_data = '';
			res.on( 'data', function( response_data ) {
				s_data += response_data;
			});
			res.on( 'end', function() {
				var reply_data = new Object();
				reply_data.blog_id = blog_id;
				if ( 200 == res.statusCode ) {
					try {
						reply_data = JSON.parse( s_data );
					}
					catch ( Exception ) {
						reply_data.status = -1;
						reply_data.reply = "PARSE_ERROR";
					}
				} else {
					reply_data.status = -1;
					reply_data.reply = "CODE_ERROR";
					logger.error( res.statusCode + ': error sending status data to ' + veriflier_server.host + ':' + veriflier_server.port );
				}
				call_back( reply_data );
			});
		};

		var error_handler = function( err ) {
			var reply_data = new Object();
			reply_data.status = -1;
			reply_data.reply = "REQUEST_ERROR";
			reply_data.blog_id = blog_id;
			call_back( reply_data );
			logger.error( 'error performing request: ' + err );
		};

		var timeout_handler = function() {
			var reply_data = new Object();
			reply_data.status = -1;
			reply_data.reply = "TIMEOUT_ERROR";
			reply_data.blog_id = blog_id;
			call_back( reply_data );
			logger.error( 'timed out performing a request to server ' + veriflier_server.host );
		};

		var request = _https.request( options ).
								 addListener( 'response', response_handler )
								.addListener( 'error', error_handler )
								.addListener( 'timeout', timeout_handler );
		request.setTimeout( NETWORK_TIMEOUT_MS );
		request.end();
	}
}

module.exports = client;

