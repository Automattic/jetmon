
process.title = 'jetmon-worker';

const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

const SUICIDE_SIGNAL        = 1;
const EXIT_MAXRAMUSAGE      = 2;
// Empirically ended up with 53MB per worker.
// They don't get killed off all the time, and on a system with 16GB RAM
// we end up having ~1.3GB free.
const MAX_PROCESS_MEM_USAGE = 53 * 1024 * 1024;
const DEFAULT_HTTP_PORT     = 80;

const JETMON_CHECK        = 1;
const VERIFLIER_CHECK     = 2;

const SECONDS = 1000;
const MINUTES = 60 * SECONDS;
const HOURS   = 60 * MINUTES;
const DAYS    = 24 * HOURS;

var _watcher = require( './jetmon.node' );
var o_log4js = require( 'log4js' );

// each worker loads it's own config object
var config   = require( './config' );
config.load();

var arrCheck      = [];
var running       = false;
var askedForWork  = false;
var suicideSignal = false;
var pointer       = 0;

/**
 * How many checks are currently being processed by the worker.
 *
 * @type {number}
 */
var activeChecks  = 0;
var createdTime   = Date.now();

var workerTotals = {};
workerTotals[SITE_DOWN] = 0;
workerTotals[SITE_RUNNING] = 0;
workerTotals[SITE_CONFIRMED_DOWN] = 0;

var checkStats = {};

o_log4js.configure( {
  appenders: [ {
		'type'      : 'file',
		'filename'  : 'logs/jetmon.log',
		'maxLogSize': 52428800,
		'backups'   : 30,
		'category'  : 'flog',
		'levels'    : 'DEBUG',
		},
		{
		'type'      : 'file',
		'filename'  : 'logs/status-change.log',
		'maxLogSize': 104857600,
		'backups'   : 100,
		'category'  : 'slog',
		'levels'    : 'DEBUG',
		}
	]
});
o_log4js.PatternLayout = '%d{HH:mm:ss,SSS} p m';

global.logger  = o_log4js.getLogger( 'flog' );
var slogger    = o_log4js.getLogger( 'slog' );

var _os         = require( 'os' );
var hostname    = _os.hostname();

var HttpChecker = {
	checkServers: function() {
		try {
			var pointerCurrentMax = pointer + config.get( 'NUM_TO_PROCESS' );
			if ( pointerCurrentMax > arrCheck.length )
				pointerCurrentMax = arrCheck.length;
			for ( ; pointer < pointerCurrentMax ; pointer++ ) {
				activeChecks++;
				_watcher.http_check( arrCheck[ pointer ].monitor_url, DEFAULT_HTTP_PORT, pointer, HttpChecker.processResultsCallback );
			}
		}
		catch ( Exception ) {
			logger.debug( process.pid + ': ERROR - failed to process the server array: ' + Exception.toString() );
		}
	},

	processResultsCallback: function( serverArrayIndex, rtt, http_code ) {
		/**
		 * Reduce the amount of active checks, as the check has finished.
		 */
		activeChecks--;

		var server = arrCheck[ serverArrayIndex ];
		server.processed = true;
		server.lastCheck = new Date().valueOf();	// we use set the value to the milliseconds value

		if ( rtt > 0 && 400 > http_code && 0 != http_code ) {
			server.site_status = SITE_RUNNING;
		}
		else if (
			( SITE_RUNNING == server.oldStatus ) ||
			(
				( SITE_CONFIRMED_DOWN != server.oldStatus ) &&
				( new Date().valueOf() < ( server.last_status_change + ( config.get( 'TIME_BETWEEN_NOTICES_MIN' ) * MINUTES ) ) )
			)
		) {
			server.site_status = SITE_DOWN;
		}
		else {
			server.site_status = SITE_CONFIRMED_DOWN;
		}

		if ( server.site_status !=  server.oldStatus ) {
			var resO    = {};
			resO.type   = JETMON_CHECK;
			resO.host   = hostname;
			resO.status = server.site_status;
			resO.rtt    = Math.round( rtt / 1000 );
			resO.code   = http_code;
			server.checks.push( resO );

			// if site is down and it has not been confirmed
			if ( server.site_status == SITE_DOWN ) {
				process.send( { msgtype: 'recheck', server: server } );
			} else if ( SITE_CONFIRMED_DOWN != server.site_status ) {
				process.send( { msgtype: 'notify_status_change', server: server } );
				slogger.trace( 'status_change: ' + JSON.stringify( server ) );
			} else {
				process.send( { msgtype: 'notify_still_down', server: server } );
				slogger.trace( 'still_down: ' + JSON.stringify( server ) );
			}
		}

		workerTotals[server.site_status]++;

		if ( pointer < arrCheck.length ) {
			activeChecks++;
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
				process.send( { msgtype: 'send_work', worker_pid: process.pid } );
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


		/**
		 * Store stats data to send to the parent later.
		 *
		 * Doing in the end to make sure we send all the data to appropriate consumers first and
		 * only then we can try to log the data.
		 */
		let stats_site_status = 'unknown';

		switch ( server.site_status ) {
			case SITE_RUNNING:
				stats_site_status = 'up';
				break;

			case SITE_DOWN:
				stats_site_status = 'down';
				break;

			case SITE_CONFIRMED_DOWN:
				stats_site_status = 'still_down';
				break;
		}

		const stats_rtt = Math.round( rtt / 1000 );

		if ( checkStats[stats_site_status] ) {
			checkStats[stats_site_status]['http_code'][http_code] = ( checkStats[stats_site_status]['http_code'][http_code] || 0 ) + 1;

			checkStats[stats_site_status]['rtt']['count']++;
			checkStats[stats_site_status]['rtt']['sum'] += stats_rtt;
			checkStats[stats_site_status]['rtt']['max']  = Math.max( checkStats[stats_site_status]['rtt']['max'], stats_rtt );
			checkStats[stats_site_status]['rtt']['min']  = Math.min( checkStats[stats_site_status]['rtt']['min'], stats_rtt );
		} else {
			checkStats[stats_site_status] = {
				'http_code': {},
				'rtt':       {
					'count': 1,
					'sum':   stats_rtt,
					'max':   stats_rtt,
					'min':   stats_rtt
				}
			};

			checkStats[stats_site_status]['http_code'][http_code] = 1;
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

	/**
	 * Returns how long the worker has been running.
	 *
	 * @returns {number}
	 */
	getAge: function() {
		return Date.now() - createdTime;
	}

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
				logger.debug( 'worker pid ' + msg.pid + ': updating config settings.' );
				config.load();
				break;
			}
			default: {
				logger.debug( process.pid + ': INFO: received unknown message "' + msg.request + '"' );
				process.send( { msgtype: 'unknown', worker_pid: msg.pid, payload: 0 } );
				break;
			}
		}
	}
	catch ( Exception ) {
		logger.error( process.pid + ": ERROR: receiving the Master's message: " + Exception.toString() );
	}
});

setInterval( function() {
	if ( workerTotals[SITE_DOWN] ||  workerTotals[SITE_RUNNING] || workerTotals[SITE_CONFIRMED_DOWN] )
		process.send( { msgtype: 'totals', worker_pid: process.pid, work_totals: workerTotals } );
	workerTotals[SITE_DOWN] = 0;
	workerTotals[SITE_RUNNING] = 0;
	workerTotals[SITE_CONFIRMED_DOWN] = 0;
}, config.get( 'STATS_UPDATE_INTERVAL_MS' ) );

setTimeout( function() {
			askedForWork = true;
			if ( true === process.connected ) {
				process.send( { msgtype: 'send_work', worker_pid: process.pid } );
			}
}, 2000 );


/**
 * Periodically send stats up to the main Jetmon process, so we can know what the current situation in the worker is.
 *
 * Currently, sending stats every 1 second.
 */
setInterval( function() {
	var message = {
		msgtype:      'stats',
		worker_pid:   process.pid,
		stats:        {
			queueLength:  arrCheck.length,
			pointer:      pointer,
			activeChecks: activeChecks,
			memoryUsage:  process.memoryUsage().rss,
			checkStats:   checkStats,
			uptime:       HttpChecker.getAge(),
		}
	};

	checkStats = {};

	process.send( message );
}, 1000 );
