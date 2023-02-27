
process.title = 'jetmon-master';

const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

const HOST_OFFLINE        = 0;
const HOST_ONLINE         = 1;

const SUICIDE_SIGNAL      = 1;
const EXIT_MAXRAMUSAGE    = 2;

const NUM_SSL_SERVERS     = 4;

const JETMON_CHECK        = 1;
const VERIFLIER_CHECK     = 2;

const STATUS_PORT         = 7802;

const SECONDS = 1000;
const MINUTES = 60 * SECONDS;
const HOURS   = 60 * MINUTES;
const DAYS    = 24 * HOURS;

// This determines how many peers have to confirm that the
// site is down before a notification email is sent
const PEER_OFFLINE_LIMIT  = 3;

global.config = require( './config' );
config.load();

var child_proc = require('child_process');
var fs         = require( 'fs' );
var o_log4js   = require( 'log4js' );

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
o_log4js.PatternLayout = '%d{HH:mm:ss,SSS} m';
global.logger = o_log4js.getLogger( 'flog' );
var slogger   = o_log4js.getLogger( 'slog' );

var db_mysql = require( './database' );
var wpcom    = require( './wpcom'    );
var comms    = require( './comms'    );
var cluster  = require( 'cluster'    );

// var http     = require( 'http'       );

var gCountSuccess   = 0;
var gCountError     = 0;
var gCountOffline   = 0;
var startTime       = new Date().valueOf();
var sitesCount      = 0;
var arrObjects      = [];
var localRetries    = [];
var freeWorkers     = [];
var arrWorkers      = [];
var workerStats     = {};
var gettingSites    = false;
var endOfRound      = false;

global.queuedRetries = [];

logger.debug( 'booting jetmon.js' );

/**
 * Used to get the hostname of the current host, so we can have per-host monitoring of things.
 */
var _os     = require( 'os' );

/**
 * Hostnames on prod look like:
 * 	<node>.<datacenter>.<domain>
 *
 * We only need the first 2 pieces and flip them around to make easier to group/filter things in StatsD later.
 * @type {string}
 */
const currentHostname = _os.hostname().split( '.' ).slice( 0, 2 ).reverse().join( '.' );

/**
 * Set up the StatD client.
 *
 * All entries are prefixed with `com.jetpack.jetmon.<hostname>.`
 */

var statsdHostname = '127.0.0.1';

/**
 * Add a workaround for the local Docker instances, as prod is running statsd proxies on 127.0.0.1,
 * while the Docker nodes run it in the `statsd` container.
 */
if ( currentHostname === 'jetmon.docker' ) {
	statsdHostname = 'statsd';
}

var StatsD = require( 'hot-shots' );
var statsdClient = new StatsD( {
	host: statsdHostname,
	port: 8125,
	globalize: true,
	prefix: 'com.jetpack.jetmon.' + currentHostname + '.',
	errorHandler: function( error ) {
		console.log( "Socket errors caught here: ", error );
		logger.debug( 'Adding error log' + error );
	},
} );

process.on( 'SIGINT',  gracefulShutdown );
process.on( 'EXIT',    gracefulShutdown );

process.on( 'SIGHUP', function() {
	logger.debug( 'reloading config file' );
	global.config.load();

	statsdClient.increment('config_reload.count');

	for ( var count in arrWorkers ) {
		if ( undefined !== arrWorkers[ count ] )
			arrWorkers[ count ].send( { pid : arrWorkers[ count ].pid, request : 'config-update' } );
	}

	// TODO this should be handled periodically, not here
	var newWorkerCount = global.config.get( 'NUM_WORKERS' );
	if ( arrWorkers.length < newWorkerCount ) {
		logger.debug( 'spawning ' + ( newWorkerCount - arrWorkers.length ) + ' new workers' );
		for ( var loop = 0; loop < ( newWorkerCount - arrWorkers.length ); loop++ ) {
			spawnWorker();
		}
	}
});

process.on( 'uncaughtException', function( errDesc ) {
	logger.debug( 'uncaughtException error: ' + errDesc );
});

function spawnWorker() {
	var worker = child_proc.fork('./lib/httpcheck.js' );

	statsdClient.increment('worker.spawn.count');

	worker.on( 'message', workerMsgCallback );
	worker.on( 'exit', function( code, signal ) {
		if ( true == worker.exitedAfterDisconnect ) {
			logger.debug( 'worker thread pid ' + worker.pid + ' shutting down.' );

			statsdClient.increment('worker.die.shutdown.count');
		} else {
			if ( SUICIDE_SIGNAL == code ) {
				logger.debug( 'worker thread pid ' + worker.pid + ' was asked to evaporate.' );
				statsdClient.increment('worker.die.evaporate.count');

			} else if ( EXIT_MAXRAMUSAGE == code ) {
				logger.debug( 'worker thread pid ' + worker.pid + ' eXited due to reaching mem limit, replacing...' );
				statsdClient.increment('worker.die.memlimit.count');

				deleteWorker( worker.pid );
				spawnWorker();
			} else {
				if ( 130 == code ) {
					logger.debug( 'worker thread pid ' + worker.pid + ' shutting down.' );
					statsdClient.increment('worker.die.code_130.count');
				} else {
					statsdClient.increment('worker.die.code_other.count');

					logger.debug( 'worker thread pid ' + worker.pid + ' died (' + code + '), creating a replacement.' );
					deleteWorker( worker.pid );
					spawnWorker();
				}
			}
		}
	} );
	arrWorkers.push( worker );
}

function deleteWorker( pid ) {
	if ( ! pid )
		return;
	for ( var count in arrWorkers ) {
		if (
			( undefined != arrWorkers[count] ) &&
			( arrWorkers[count].pid == pid )
		) {
			arrWorkers.splice( count, 1 );
			if ( workerStats[pid] ) {
				delete( workerStats[pid] );
			}

			statsdClient.increment('worker.delete.count');

			break;
		}
	}
}

function getWorker( pid ) {
	if ( ! pid )
		return null;
	for ( var count in arrWorkers ) {
		if ( ( undefined != arrWorkers[ count ] ) &&
			( arrWorkers[ count ].pid == pid ) ) {
			return arrWorkers[ count ];
		}
	}
	return null;
}

function gracefulShutdown() {
	// Note: calling the 'logger' object during shutdown causes an immediate exit (only use 'console.log')
	console.log( 'Caught shutdown signal, disconnecting worker threads.' );
	for ( var count in arrWorkers ) {
		if ( undefined !== arrWorkers[ count ] && arrWorkers[ count ].connected ) {
			arrWorkers[ count ].disconnect();
		}
	}

	console.log( 'committing any outstanding db updates.' );
	db_mysql.commitUpdates(
		function() {
			printTotalsExit();
			process.exit( 0 );
	});
}

function printTotalsExit() {
	printTotals();
	process.exit( 0 );
}

function printTotals() {
	console.log( '' );
	console.log( 'Error:   ' + gCountError );
	console.log( 'Offline: ' + gCountOffline );
	console.log( 'Success: ' + gCountSuccess );
	console.log( 'Total:   ' + ( gCountSuccess + gCountError + gCountOffline ) );
	var now = new Date().valueOf();
	console.log( 'Time:    ' + Math.floor( ( now - startTime ) / 60000 ) + 'm ' +
				( ( ( now - startTime ) % 60000 ) / 1000 ) + 's' );
}

function resetVariables() {
	gCountSuccess = 0;
	gCountError   = 0;
	gCountOffline = 0;
	startTime     = new Date().valueOf();
	endOfRound    = false;
	queuedRetries = [];
	localRetries  = [];
}

function getMoreSites() {
	gettingSites = true;
	if ( endOfRound ) {
		var timeToNextLoop = ( global.config.get( 'MIN_TIME_BETWEEN_ROUNDS_SEC' ) * SECONDS ) - ( new Date().valueOf() - startTime );
		setTimeout( function() {
				resetVariables();
				getMoreSites();
			},
			timeToNextLoop
		);
		return;
	}

	/**
	 * Write out how many items were still in the queue when we requested new batch of data
	 */
	statsdClient.increment( 'queue.items_left_in_queue_when_fetching_new.count', arrObjects.length );

	const startTimeGetDbBatch = new Date().valueOf();

	endOfRound = db_mysql.getNextBatch(
		function( rows ) {
			if ( ( undefined === rows ) || ( 0 === rows.length ) ) {
				getMoreSites();
				return;
			}

			const endTimeGetDbBatch = new Date().valueOf();
			statsdClient.timing( 'db.get_next_batch', endTimeGetDbBatch - startTimeGetDbBatch );

			for ( var i = 0; i < rows.length; i++ ) {
				var server = rows[i];
				server.processed = false;
				server.oldStatus = server.site_status;
				server.last_status_change = new Date( server.last_status_change ).valueOf();
				server.checks = [];
				arrObjects.push( server );
			}
			gettingSites = false;
			freeWorkersToWork();
	});
}

function freeWorkersToWork() {
	if ( 0 == arrObjects.length )
		return;
	var tmpWorkers = freeWorkers; 	// take pointer
	freeWorkers = [];				// and reset
	for ( var i = 0; i < tmpWorkers.length; i++ )
		if ( null !== getWorker( tmpWorkers[i] ) )
			workerMsgCallback( { msgtype: 'send_work', worker_pid: tmpWorkers[i] } );
}

function checkHostStatus( veriflier_host, data ) {
	for( var loop = 0; loop < queuedRetries.length; loop++ ) {
		if ( queuedRetries[ loop ].blog_id != data.blog_id ) {
			continue;
		}
		queuedRetries[ loop ].requests_outstanding--;
		queuedRetries[ loop ].last_activity = new Date().valueOf();
		var replyO    = {};
		replyO.type   = VERIFLIER_CHECK;
		replyO.host   = veriflier_host;
		replyO.status = data.status;
		replyO.rtt    = data.rtt;
		replyO.code   = data.code;
		queuedRetries[ loop ].checks.push( replyO );
		if ( HOST_OFFLINE == data.status ) {
			queuedRetries[ loop ].offline_confirms++;
			if ( queuedRetries[ loop ].offline_confirms >= PEER_OFFLINE_LIMIT ) {
				queuedRetries[ loop ].site_status = SITE_DOWN;
				wpcom.notifyStatusChange( queuedRetries[ loop ],
						function( reply ) {
							if ( ! reply.success ) {
								logger.error( 'error posting status change, retrying...' );
								wpcom.notifyStatusChange( queuedRetries[ loop ],
										function( reply ) {
											if ( reply.success )
												logger.trace( 'posted successfully' );
											else
												logger.error( 'error posting status change.' );
								});
							}
				});
				slogger.trace( 'site_down: ' + JSON.stringify( queuedRetries[ loop ] ) );
			}
		}
		break;
	}
}

function sslWorkerCallBack( msg ) {
	try {
		switch ( msg.msgtype ) {
			case 'host_status': {
				checkHostStatus( msg.payload.veriflier_host, msg.payload );
				break;
			}
			case 'host_status_array': {
				for( var loop = 0; loop < msg.payload.checks.length; loop++ ) {
					checkHostStatus( msg.payload.veriflier_host, msg.payload.checks[ loop ] );
				}
				break;
			}
			default: {
				logger.debug( 'Unknown SSL worker message type: ' + msg.msgtype );
				break;
			}
		}
	}
	catch ( Exception ) {
		logger.error( "Error receiving SSL worker's message: " + Exception.toString() );
	}
}

function workerMsgCallback( msg ) {
	try {
		switch ( msg.msgtype ) {
			case 'totals':
				gCountSuccess += msg.work_totals[SITE_RUNNING];
				gCountError += msg.work_totals[SITE_DOWN];
				gCountOffline += msg.work_totals[SITE_CONFIRMED_DOWN];
				sitesCount += msg.work_totals[SITE_DOWN] + msg.work_totals[SITE_RUNNING] + msg.work_totals[SITE_CONFIRMED_DOWN];
				break;
			case 'notify_still_down':
				// set new server status and then send via the next case statement
				msg.server.site_status = SITE_CONFIRMED_DOWN;
			case 'notify_status_change':
				wpcom.notifyStatusChange( msg.server,
						function( reply ) {
							if ( ! reply.success ) {
								logger.error( 'error posting status change, retrying...' );
								wpcom.notifyStatusChange( msg.server,
										function( reply ) {
											if ( reply.success )
												logger.trace( 'posted successfully' );
											else
												logger.error( 'error posting status change.' );
								});
							}
				});
				break;
			case 'send_work':
				/**
				 * Worker asked for work
				 */

				/**
				 * There are more workers than needed, kindly ask the worker to shut down.
				 */
				if ( arrWorkers.length > global.config.get( 'NUM_WORKERS' ) ) {
					var w = getWorker( msg.worker_pid );
					if ( null !== w )
						w.send( {
							pid     : msg.worker_pid,
							request : 'evaporate',
							payload : 'pls :)'
						} );
					break;
				}

				if ( 0 == arrObjects.length ) {
					/**
					 * There are no URLs in the global queue, let's flag the worker as "free"
					 * and request more sites from the database, if we haven't done so yet.
					 */
					if ( -1 == freeWorkers.indexOf( msg.worker_pid ) ) {
						freeWorkers.push( msg.worker_pid );
					}
					if ( ! gettingSites ) {
						gettingSites = true;
						getMoreSites();
					}
				} else {
					/**
					 * There are items in the global queue, let's send them to the worker.
					 */
					var w = getWorker( msg.worker_pid );
					if ( null !== w )
						w.send( {
							pid     : msg.worker_pid,
							request : 'queue-add',
							payload : arrObjects.splice( 0, Math.min( arrObjects.length, global.config.get( 'DATASET_SIZE' ) ) )
					} );
				}
				break;
			case 'recheck':
				if ( msg.server.checks.length < config.get( 'NUM_OF_CHECKS' ) ) {
					msg.server.processed = false;
					localRetries.push( msg.server );
				} else {
					// we have exhausted our local check limit, ask the verifliers to confirm
					host_check_request( msg.server );
				}
				break;

			case 'stats':
				if ( msg.stats ) {
					workerStats[msg.worker_pid] = msg.stats;
				}
				break;
			default:
		}
	}
	catch ( Exception ) {
		logger.error( "Error receiving worker's message: ", Exception, msg );
	}
}

function host_check_request( server ) {
	var check_server = {};
	check_server.blog_id              = server.blog_id;
	check_server.monitor_url          = server.monitor_url;
	check_server.status_id            = server.site_status;
	check_server.lastCheck            = server.lastCheck;
	check_server.last_status_change   = server.last_status_change;
	check_server.checks               = server.checks;
	check_server.offline_confirms     = 0;
	check_server.requests_sent        = false;
	check_server.requests_outstanding = 0;
	check_server.last_activity        = new Date().valueOf();

	queuedRetries.push( check_server );
}

function updateStats() {
	try {
		if ( true === global.config.get( 'DEBUG' ) ) {
			var nextLoop = ( global.config.get( 'MIN_TIME_BETWEEN_ROUNDS_SEC' ) * SECONDS ) - ( new Date().valueOf() - startTime );
			logger.debug( 'sps = ' + sitesCount + ' - ' + ( arrWorkers.length - freeWorkers.length ) + ' working, ' +
							freeWorkers.length + ' waiting : next round in ' + ( nextLoop / 1000 ) + 's' );
			if ( nextLoop < -20000 ) {
				logger.error( 'restarting the getMoreSites loop' );
				resetVariables();
				setTimeout( getMoreSites, 100 );
			}
		}
		var localCount = sitesCount; // need this local otherwise the async call below writes 0, due to the 'finally' call setting sitesCount to 0
		var spsFile = fs.createWriteStream( 'stats/sitespersec', { flags : "w" } );
		spsFile.once( 'open', function( fd ) {
			spsFile.write( 'sites per second: ' + localCount + '\n' );
			spsFile.end();
		});
		var queueFile = fs.createWriteStream( 'stats/sitesqueue', { flags : "w" } );
		queueFile.once( 'open', function( fd ) {
			queueFile.write( 'sites in queue: ' + arrObjects.length + '\n' );
			queueFile.end();
		});
		var totalFile = fs.createWriteStream( 'stats/totals', { flags : "w" } );
		totalFile.once( 'open', function( fd ) {
			totalFile.write( 'working : ' + ( arrWorkers.length - freeWorkers.length ) + '\n' );
			totalFile.write( 'waiting : ' + freeWorkers.length + '\n' );
			totalFile.write( 'error   : ' + gCountError + '\n' );
			totalFile.write( 'offline : ' + gCountOffline + '\n' );
			totalFile.write( 'total   : ' + gCountSuccess + '\n' );
			totalFile.end();
		});

		statsdClient.increment( 'stats.sites.sps.count', sitesCount );
		statsdClient.increment( 'stats.sites.error.count', gCountError );
		statsdClient.increment( 'stats.sites.offline.count', gCountOffline );
		statsdClient.increment( 'stats.sites.total.count', gCountSuccess );
		statsdClient.increment( 'stats.sites.queue.count', arrObjects.length );

		statsdClient.increment( 'stats.workers.free.count', freeWorkers.length );
		statsdClient.increment( 'stats.workers.working.count', ( arrWorkers.length - freeWorkers.length ) );

	}
	catch  ( Exception ) {
		logger.error( 'Error updating stats files: ' + Exception.toString() );
	}
	finally {
		sitesCount = 0;
		setTimeout( updateStats, ( global.config.get( 'STATS_UPDATE_INTERVAL_MS' ) ) );
	}
}

function processQueuedRetries() {
	if ( true === global.config.get( 'DEBUG' ) )
		logger.debug( 'starting checks for ' + queuedRetries.length + ' REMOTE' );

	var sendRetries = [];
	var peerCount = global.config.get( 'VERIFIERS' ).length;
	for( var loop = queuedRetries.length - 1; loop >= 0; loop-- ) {
		if ( false === queuedRetries[loop].requests_sent ) {
			sendRetries.push( queuedRetries[loop] );
			queuedRetries[loop].requests_sent = true;
			queuedRetries[loop].requests_outstanding = peerCount;
		} else if ( ( queuedRetries[loop].requests_outstanding <= 0 ) ||
			( new Date().valueOf() > queuedRetries[loop].last_activity + ( global.config.get( 'TIMEOUT_FOR_REQUESTS_SEC' ) * SECONDS ) ) ) {
			if ( true === global.config.get( 'DEBUG' ) ) {
				if ( 0 < queuedRetries[loop].requests_outstanding )
					logger.trace( 'TIMED out : ' + queuedRetries[loop].monitor_url +
									', "outstanding": ' + queuedRetries[loop].requests_outstanding +
									', "confirms": ' + queuedRetries[loop].offline_confirms );
				else
					logger.trace( 'NORMAL out : ' + queuedRetries[loop].monitor_url +
									', "outstanding": ' + queuedRetries[loop].requests_outstanding +
									', "confirms": ' + queuedRetries[loop].offline_confirms );
			}
			queuedRetries.splice( loop, 1 );
		}
	}

	var peerArray = global.config.get( 'VERIFIERS' );
	var batchSize = global.config.get( 'VERIFLIER_BATCH_SIZE' ) || 200;
	for( var loop = sendRetries.length - 1; loop >= 0; loop -= batchSize ) {
		var sending = Math.min( batchSize, sendRetries.length );
		var batchData = sendRetries.splice( sendRetries.length - sending, sending );
		for ( var count in peerArray ) {
			comms.get_remote_status_array(
				peerArray[ count ],
				batchData,
				function( res ) {
					if ( 1 !== res.status ) {
						logger.debug( res.veriflier + ': send ' + res.status );
					}
			});
		}
	}

	var addedWork = false;
	if ( true === global.config.get( 'DEBUG' ) )
		logger.debug( 'starting checks for ' + localRetries.length + ' LOCAL' );
	for( var loop = localRetries.length - 1; loop >= 0; loop-- ) {
		if ( new Date().valueOf() < ( localRetries[loop].lastCheck + ( global.config.get( 'TIME_BETWEEN_CHECKS_SEC' ) * SECONDS ) ) )
			continue;
		if ( 0 !== freeWorkers.length ) {
			var i = 0;
			while ( i < freeWorkers.length && null === getWorker( freeWorkers[i] ) ) {
				i++;
			}
			if ( i < freeWorkers.length ) {
				var w = getWorker( freeWorkers[i] );
				w.send( {
					pid     : freeWorkers[i],
					request : 'queue-add',
					payload : [ localRetries.splice( loop, 1 )[0] ]
				} );
				freeWorkers.splice( i, 1 );
			} else {
				arrObjects.push( localRetries.splice( loop, 1 )[0] );
				addedWork = true;
			}
		} else {
			arrObjects.push( localRetries.splice( loop, 1 )[0] );
			addedWork = true;
		}
	}
	if ( addedWork )
		freeWorkersToWork();
}

// Create all our workers
for ( var loop = 0; loop < global.config.get( 'NUM_WORKERS' ); loop++ ) {
	spawnWorker();
}

// Start the SSL cluster
cluster.setupMaster( {
	exec   : './lib/server',
	silent : false,
});

cluster.on( 'online', function( worker ) {
	logger.debug( 'SSL worker (pid:' + worker.process.pid + ') is online.' );
});

cluster.on( 'disconnect', function( worker ) {
	logger.debug( 'SSL worker (pid:' + worker.process.pid + ') has disconnected.' );
});

cluster.on( 'exit', function( worker, code, signal ) {
	if ( true == worker.exitedAfterDisconnect ) {
		logger.debug( 'SSL worker (pid:' + worker.process.pid + ') is shutting down.' );
	} else {
		logger.error( 'SSL worker (pid:' + worker.process.pid + ') died (' + worker.process.exitCode + ').' );
	}
});

for ( var i = 0; i < NUM_SSL_SERVERS; i++ ) {
	var ssl_server = cluster.fork();
	ssl_server.on( 'message', sslWorkerCallBack );
}

// set a repeating 'tick' to perform clean-up and retries allocation
setInterval( processQueuedRetries, SECONDS * 5 );

// start the 'recursive' stats logging
updateStats();

