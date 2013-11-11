
process.title = 'jetmon-worker';

var cluster  = require( 'cluster' );
var config   = require( './config' );
var _watcher = require( './jetmon.node' );
var o_log4js = require( 'log4js' );

var arrCheck     = new Array();
var running	     = false;
var askedForWork = false;
var pointer      = 0;

const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

o_log4js.configure( {
  appenders: [ {
		'type'      : 'file',
		'filename'  : 'logs/jetmon.log',
		'maxLogSize': 10485760,
		'backups'   : 10,
		'category'  : 'flog',
		'levels'    : 'DEBUG',
		}
	]
});
o_log4js.PatternLayout = '%d{HH:mm:ss,SSS} p m';

var logger = o_log4js.getLogger( 'flog' );

var HttpChecker = {
	checkServers: function () {
		try {
			var pointerCurrentMax = pointer + config.NUM_TO_PROCESS;
			if ( pointerCurrentMax > arrCheck.length )
				pointerCurrentMax = arrCheck.length;

			for ( ; pointer < pointerCurrentMax ; pointer++ ) {
				_watcher.http_check( arrCheck[ pointer ].monitor_url, config.HTTP_PORT, pointer, HttpChecker.processResultsCallback );
			}
		}
		catch ( Exception ) {
			logger.debug( cluster.worker.id + ': ERROR - failed to process the server array: ' + Exception.toString() );
		}
	},

	processResultsCallback: function( serverArrayIndex, rtt, http_code ) {
		var server = arrCheck[ serverArrayIndex ];
		server.rtt = rtt;
		server.processed = true;
		server.checked++;

		if ( server.rtt > 0 && 400 > http_code )
			server.site_status = SITE_RUNNING;
		else if ( ( SITE_RUNNING == server.oldStatus ) ||
				( SITE_CONFIRMED_DOWN != server.oldStatus ) && ( new Date().valueOf() < ( server.last_status_change + config.TIME_BETWEEN_NOTIFICATIONS ) ) )
			server.site_status = SITE_DOWN;
		else
			server.site_status = SITE_CONFIRMED_DOWN;

		if ( server.site_status !=  server.oldStatus ) {
			// if site is down and it has not been confirmed
			if ( ( server.site_status == SITE_DOWN ) && ( server.checked < config.NUM_OF_CHECKS ) ) {
				server.lastCheck = new Date().valueOf();	// we use set the value to the milliseconds value
				process.send( { msgtype: 'recheck', server: server } );
			} else if ( SITE_CONFIRMED_DOWN != server.site_status ) {
				process.send( { msgtype: 'notify_status_change', server: server } );
			} else {
				process.send( { msgtype: 'notify_still_down', server: server } );
			}
		}

		process.send( { msgtype: 'totals', workerid: cluster.worker.id, server: server } );

		if ( pointer < arrCheck.length ) {
			_watcher.http_check( arrCheck[ pointer ].monitor_url, config.HTTP_PORT, pointer, HttpChecker.processResultsCallback );
			pointer++;
		} else {
			// check if we have any outstanding callbacks
			var waiting_for = 0;
			for ( var count in arrCheck ) {
				if ( ! arrCheck[ count ].processed )
					waiting_for++;
			}
			// if we have a smaller number than we can process at the same time, ask for more work
			if ( waiting_for < config.NUM_TO_PROCESS ) {
				if ( 0 === waiting_for )
					running = false;
				if ( ! askedForWork ) {
					askedForWork = true;
					process.send( { msgtype: 'send_work', workerid: cluster.worker.id } );
				}
			}
		}
	},

	addToQueue: function( arrData ) {
		if ( running ) {
			for ( var count in arrData ) {
				arrCheck.push( arrData[ count ] );
			}
		} else {
			arrCheck = arrData;
			pointer = 0;
			running	 = true;
			setTimeout( HttpChecker.checkServers, 50 );
		}
	},

};

process.on( 'message', function( msg ) {
	try {
		switch ( msg.request )
		{
			case 'queue-add': {
				// once we get some work we reset our 'asked state'
				askedForWork = false;
				HttpChecker.addToQueue( msg.payload );
				break;
			}
			default: {
				logger.debug( cluster.worker.id + ': INFO: received unknown message "' + msg.request + '"' );
				process.send( { msgtype: 'unknown', workerid: msg.id, payload: 0 } );
				break;
			}
		}
	}
	catch ( Exception ) {
		logger.error( cluster.worker.id + ": ERROR: receiving the Master's message: " + Exception.toString() );
	}
});

setTimeout( function() {
			askedForWork = true;
			process.send( { msgtype: 'send_work', workerid: cluster.worker.id } );
}, 2000 );
