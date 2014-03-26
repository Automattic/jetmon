
process.title = 'jetmon-worker';

const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

const SUICIDE_SIGNAL        = 1;
const EXIT_MAXRAMUSAGE      = 2;
const MAX_PROCESS_MEM_USAGE = 40 * 1024 * 1024;
const DEFAULT_HTTP_PORT     = 80;

const SECONDS = 1000;
const MINUTES = 60 * SECONDS;
const HOURS   = 60 * MINUTES;
const DAYS    = 24 * HOURS;

var cluster  = require( 'cluster' );
var _watcher = require( './jetmon.node' );
var o_log4js = require( 'log4js' );

// each worker loads it's own config object
var config   = require( './config' );
config.load();

var arrCheck      = new Array();
var running	      = false;
var askedForWork  = false;
var suicideSignal = false;
var pointer       = 0;

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
	checkServers: function() {
		try {
			var pointerCurrentMax = pointer + config.get( 'NUM_TO_PROCESS' );
			if ( pointerCurrentMax > arrCheck.length )
				pointerCurrentMax = arrCheck.length;
			for ( ; pointer < pointerCurrentMax ; pointer++ ) {
				_watcher.http_check( arrCheck[ pointer ].monitor_url, DEFAULT_HTTP_PORT, pointer, HttpChecker.processResultsCallback );
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
		server.lastCheck = new Date().valueOf();	// we use set the value to the milliseconds value
		server.checked++;

		if ( server.rtt > 0 && 400 > http_code )
			server.site_status = SITE_RUNNING;
		else if ( ( SITE_RUNNING == server.oldStatus ) ||
				( SITE_CONFIRMED_DOWN != server.oldStatus ) && ( new Date().valueOf() < ( server.last_status_change + ( config.get( 'TIME_BETWEEN_NOTICES_MIN' ) * MINUTES ) ) ) )
			server.site_status = SITE_DOWN;
		else
			server.site_status = SITE_CONFIRMED_DOWN;

		if ( server.site_status !=  server.oldStatus ) {
			// if site is down and it has not been confirmed
			if ( server.site_status == SITE_DOWN ) {
				process.send( { msgtype: 'recheck', server: server } );
			} else if ( SITE_CONFIRMED_DOWN != server.site_status ) {
				process.send( { msgtype: 'notify_status_change', server: server } );
			} else {
				process.send( { msgtype: 'notify_still_down', server: server } );
			}
		}

		process.send( { msgtype: 'totals', workerid: cluster.worker.id, site_status: server.site_status } );

		if ( pointer < arrCheck.length ) {
			_watcher.http_check( arrCheck[ pointer ].monitor_url, DEFAULT_HTTP_PORT, pointer, HttpChecker.processResultsCallback );
			pointer++;
		} else {
			 // check if we have any outstanding callbacks
            var waiting_for = 0;
            for ( var count in arrCheck ) {
                if ( ! arrCheck[ count ].processed )
                    waiting_for++;
            }

			if ( ( process.memoryUsage().rss < MAX_PROCESS_MEM_USAGE ) && ( false === askedForWork ) ) {
				askedForWork = true;
				process.send( { msgtype: 'send_work', workerid: cluster.worker.id } );
			}

			if ( 0 === waiting_for ) {
				if ( suicideSignal )
					process.exit( SUICIDE_SIGNAL );

				// If we never asked for work, then it was due to being over the ram limit, to the ether for me...
				if ( false === askedForWork )
					process.exit( EXIT_MAXRAMUSAGE );

				arrCheck = [];
				running = false;
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
			case 'evaporate' : {
				if ( ! running )
					process.exit( SUICIDE_SIGNAL );
				else
					suicideSignal = true;
				break;
			}
			case 'config-update': {
				logger.debug( 'worker #' + msg.id + ': updating config settings.' );
				config.load();
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
