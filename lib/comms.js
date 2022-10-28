
var _https = require( 'https' );
var _fs    = require( 'fs' );

const NETWORK_TIMEOUT_MS = 30000;

var ssl_key  = _fs.readFileSync( 'certs/jetmon.key' );
var ssl_cert = _fs.readFileSync( 'certs/jetmon.crt' );

var client = {
	get_remote_status: function( veriflier_server, blog_id, s_server, call_back ) {
		var o_request         = {};
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

		client.perform_request( options, null, call_back );
	},

	get_remote_status_array: function( veriflier_server, checkArray, call_back ) {
		var o_request        = {};
		o_request.auth_token = global.config.get( 'AUTH_TOKEN' );
		o_request.checks     = checkArray;
		var requestData      = JSON.stringify( o_request );

		logger.debug( 'POSTing ' + checkArray.length + ' to ' +
					veriflier_server.name + ', ' + requestData.length + ' bytes' );

		var options = {
			hostname: veriflier_server.host,
			port:     veriflier_server.port,
			key:      ssl_key,
			cert:     ssl_cert,
			path:     '/get/host-status',
			method:   'POST',
			headers: {
				  'Content-Type'  : 'application/json',
				  'Content-Length': requestData.length,
			},
			rejectUnauthorized: false,
		};

		client.perform_request( options, requestData, call_back );
	},

	perform_request: function( options, postData, call_back ) {
		var response_handler = function( res ) {
			res.setEncoding( 'utf8' );
			var s_data = '';
			res.on( 'data', function( response_data ) {
				s_data += response_data;
			});
			res.on( 'end', function() {
				var reply_data = {};
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
					logger.error( res.statusCode + ': error sending status data to ' +
									options.hostname + ':' + options.port );
				}
				call_back( reply_data );
			});
		};

		var error_handler = function( err ) {
			var reply_data = {};
			reply_data.status = -1;
			reply_data.reply = "REQUEST_ERROR";
			call_back( reply_data );
			logger.error( 'error performing request: ' + err );
		};

		var timeout_handler = function() {
			var reply_data = {};
			reply_data.status = -1;
			reply_data.reply = "TIMEOUT_ERROR";
			call_back( reply_data );
			logger.error( 'timed out performing a request to server ' + options.hostname );
		};

		options.secureProtocol = "TLSv1_2_method";

		var request = _https.request( options ).
								 addListener( 'response', response_handler )
								.addListener( 'error', error_handler )
								.addListener( 'timeout', timeout_handler );
		request.setTimeout( NETWORK_TIMEOUT_MS );
		if ( null !== postData ) {
			request.write( postData );
		}
		request.end();
	}
}

module.exports = client;
