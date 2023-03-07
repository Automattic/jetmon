
const NETWORK_TIMEOUT_MS = 20000;

var https   = require( 'https' );
var fs      = require( 'fs'    );
var ssl_key  = fs.readFileSync( 'certs/jetmon.key' );
var ssl_cert = fs.readFileSync( 'certs/jetmon.crt' );

var wpcom = {
	notifyStatusChange : function( serverObject, callBack ) {
		try {
			var o_request                = {};
			o_request.blog_id            = serverObject.blog_id;
			o_request.monitor_url        = serverObject.monitor_url;
			o_request.status_id          = serverObject.site_status;
			o_request.last_check         = new Date( serverObject.lastCheck );
			o_request.last_status_change = serverObject.last_status_change ? new Date( serverObject.last_status_change ) : null;
			o_request.checks             = serverObject.checks;
			o_request.token              = global.config.get( 'AUTH_TOKEN' );

			var request_str = JSON.stringify( o_request );

			var options = {
				hostname: 'jetpack.wordpress.com',
				port:     443,
				key:      ssl_key,
				cert:     ssl_cert,
				path:     '/jetmon/?data=' + request_str,
				method:   'GET',
				rejectUnauthorized: false,
			};

			logger.trace( 'setting blogid ' + o_request.blog_id + ' status ' + o_request.status_id + ', URL: ' + o_request.monitor_url );

			var response_handler = function( res ) {
				res.setEncoding( 'utf8' );
				var reply_data = {};

				if ( 200 == res.statusCode ) {
					res.on( 'data', function ( response_data ) {
						try {
							reply_data = JSON.parse( response_data );
						}
						catch ( Exception ) {
							logger.error( 'error parsing the server response.' );
							reply_data.success = false;
						}
						callBack( reply_data );
					});
				} else {
					logger.error( 'incorrect status code from the server: ' + res.statusCode );
					reply_data.success = false;
					callBack( reply_data );
				}
			};

			var error_handler = function( err ) {
				logger.error( 'error performing request: ' + err );
				var reply_data = {};
				reply_data.success = false;
				callBack( reply_data );
			};

			var timeout_handler = function() {
				logger.error( 'timed out performing a request to the jetpack.wordpress.com server ' );
				var reply_data = {};
				reply_data.success = false;
				callBack( reply_data );
			};

			var request = https.request( options ).
									 addListener( 'response', response_handler )
									.addListener( 'error', error_handler )
									.addListener( 'timeout', timeout_handler );
			request.setTimeout( NETWORK_TIMEOUT_MS );
			request.end();
		}
		catch ( Exception ) {
			logger.error( 'notifyStatusChange error: ' + Exception.toString() );
		}
	}
}

module.exports = wpcom;

