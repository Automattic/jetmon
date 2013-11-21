
process.title = 'jetmon-master';

const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

const SUICIDE_SIGNAL      = 1;

const SECONDS = 1000;
const MINUTES = 60 * SECONDS;
const HOURS   = 60 * MINUTES;
const DAYS    = 24 * HOURS;

global.config = require( './config' );
config.load();

var cluster   = require( 'cluster' );
var fs        = require( 'fs' );
var o_log4js  = require( 'log4js' );

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
global.logger = o_log4js.getLogger( 'flog' );

var db_mysql  = require( './database' );
var postman   = require( './mailer' );

var gCountSuccess   = 0;
var gCountError     = 0;
var gCurrentWorkers = 0;
var startTime       = new Date().valueOf();
var sitesCount      = 0;
var arrObjects      = new Array();
var freeWorkers     = new Array();
var queuedRetries   = new Array();
var gettingSites    = false;
var endOfRound      = false;

logger.debug( 'booting jetmon.js' );

process.on( 'SIGINT',  gracefulShutdown );
process.on( 'EXIT',    gracefulShutdown );

process.on( 'SIGHUP', function() {
	logger.debug( 'reloading config file' );
	global.config.load();

	for ( var workerid in cluster.workers )
		cluster.workers[ workerid ].send( { id : workerid, request : 'config-update' } );

	var newWorkerCount = global.config.get( 'NUM_WORKERS' );
	if ( gCurrentWorkers < newWorkerCount ) {
		logger.debug( 'spawning ' + ( newWorkerCount - gCurrentWorkers ) + ' new workers' );
		for ( var loop = 0; loop < ( newWorkerCount - gCurrentWorkers ); loop++ ) {
			var worker = cluster.fork();
			worker.on( 'message', workerMsgCallback );
		}
		gCurrentWorkers = newWorkerCount;
	}
});

process.on( 'SIGUSR2', function() {
	// Test param settings

});

process.on( 'uncaughtException', function( errDesc ) {
	logger.debug( 'uncaughtException error: ' + errDesc );
});

cluster.setupMaster( {
	exec   : './lib/httpcheck.js',
	silent : false
});

cluster.on( 'exit', function( worker, code, signal ) {
	if ( true == worker.suicide ) {
		logger.debug( 'worker thread #' + worker.id + ' shutting down.' );
	} else {
		var exitCode = worker.process.exitCode;
		if ( SUICIDE_SIGNAL == exitCode ) {
			logger.debug( 'worker thread #' + worker.id + ' was asked to evaporate.' );
		} else {
			if ( 130 == exitCode ) {
				logger.debug( 'worker thread #' + worker.id + ' shutting down.' );
			} else {
				logger.debug( 'worker thread #' + worker.id + ' (pid:' + worker.process.pid + ') died (' + exitCode + '), creating a replacement.' );
				var worker = cluster.fork();
				worker.on( 'message', workerMsgCallback );
			}
		}
	}
});

function gracefulShutdown() {
	// Note: calling the 'logger' object during shutdown causes an immediate exit (only use 'console.log')
	console.log( 'Caught shutdown signal, disconnecting worker threads.' );
	for ( var workerid in cluster.workers )
		cluster.workers[ workerid ].disconnect();

	console.log( 'committing any outstanding db updates.' );
	db_mysql.commitUpdates( function() {
								printTotalsExit();
								process.exit( 0 );
	});
}

function printTotalsExit() {
	printTotals();
	if ( global.config.get( 'sendmails' ) )
		postman.exit();
	process.exit( 0 );
}

function printTotals() {
	console.log( '' );
	console.log( 'Error:   ' + gCountError );
	console.log( 'Success: ' + gCountSuccess );
	console.log( 'Total:   ' + ( gCountSuccess + gCountError ) );
	var now = new Date().valueOf();
	console.log( 'Time:    ' + Math.floor( ( now - startTime ) / 60000 ) + 'm ' + ( ( ( now - startTime ) % 60000 ) / 1000 ) + 's' );
}

function resetVariables() {
	gCountSuccess = 0;
	gCountError   = 0;
	startTime     = new Date().valueOf();
	endOfRound    = false;
	postman.emptyTemplatesCache();
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
	endOfRound = db_mysql.getNextBatch( function( rows ) {
									if ( ( undefined === rows ) || ( 0 === rows.length ) ) {
										getMoreSites();
										return;
									}

									for ( var i = 0; i < rows.length; i++ ) {
										var server = rows[i];
										server.processed = false;
										server.checked = 0;
										server.rtt = 0;
										server.oldStatus = server.site_status;
										server.last_status_change = new Date( server.last_status_change ).valueOf();
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
		workerMsgCallback( { msgtype: 'send_work', workerid: tmpWorkers[i] } );
}

function workerMsgCallback( msg ) {
	try {
		switch ( msg.msgtype ) {
			case 'totals':
				if ( msg.server.site_status )
					gCountSuccess++;
				else
					gCountError++;
				sitesCount++;
				break;
			case 'notify_status_change':
				db_mysql.saveNewStatus( msg.server );
				if ( global.config.get( 'sendmails' ) ) {
					postman.sendStatusChangeMail( msg.server );
				}
				break;
			case 'notify_still_down':
				if ( global.config.get( 'sendmails' ) ) {
					msg.server.site_status = SITE_CONFIRMED_DOWN;
					db_mysql.saveNewStatus( msg.server );
					postman.sendStillDownMail( msg.server );
				}
				break;
			case 'send_work':
				if ( true === global.config.get( 'DEBUG' ) )
					logger.debug( 'send_work: ' + msg.workerid + ', have ' + arrObjects.length + ' items queued' );
				if ( gCurrentWorkers > global.config.get( 'NUM_WORKERS' ) ) {
					cluster.workers[ msg.workerid ].send( { id      : msg.workerid,
															request : 'evaporate',
															payload : 'pls :)'
														} );
					gCurrentWorkers--;
					break;
				}
				if ( 0 == arrObjects.length ) {
					if ( -1 == freeWorkers.indexOf( msg.workerid ) )
						freeWorkers.push( msg.workerid );
					if ( ! gettingSites ) {
						gettingSites = true;
						getMoreSites();
					}
				} else {
					cluster.workers[ msg.workerid ].send( { id      : msg.workerid,
															request : 'queue-add',
															payload : arrObjects.splice( 0, Math.min( arrObjects.length, global.config.get( 'DATASET_SIZE' ) ) )
														} );
				}
				break;
			case 'recheck':
				msg.server.processed = false;
				queuedRetries.push( msg.server );
			default:
		}
	}
	catch ( Exception ) {
		logger.debug( "Error receiving worker's message: ", Exception, msg );
	}
}

function updateStats() {
	try {
		if ( true === global.config.get( 'DEBUG' ) ) {
			var nextLoop = ( global.config.get( 'MIN_TIME_BETWEEN_ROUNDS_SEC' ) * SECONDS ) - ( new Date().valueOf() - startTime );
			logger.debug( 'sps = ' + sitesCount + ' - ' + ( global.config.get( 'NUM_WORKERS' ) - freeWorkers.length ) + ' working, ' +
							freeWorkers.length + ' waiting : next round in ' + ( nextLoop / 1000 ) + 's' );
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
			totalFile.write( 'working : ' + ( global.config.get( 'NUM_WORKERS' ) - freeWorkers.length ) + '\n' );
			totalFile.write( 'waiting : ' + freeWorkers.length + '\n' );
			totalFile.write( 'error   : ' + gCountError + '\n' );
			totalFile.write( 'total   : ' + gCountSuccess + '\n' );
			totalFile.end();
		});
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
		logger.debug( 'starting checks for ' + queuedRetries.length + ' queued retries' );
	for( var loop = queuedRetries.length - 1; loop >= 0; loop-- ) {
		if ( new Date().valueOf() > ( queuedRetries[loop].lastCheck + ( global.config.get( 'TIME_BETWEEN_CHECKS_SEC' ) * SECONDS ) ) )
			arrObjects.push( queuedRetries.splice( loop, 1 )[0] );
	}
	freeWorkersToWork();
}

// Create all our workers
for ( var loop = 0; loop < global.config.get( 'NUM_WORKERS' ); loop++ ) {
	var worker = cluster.fork();
	worker.on( 'message', workerMsgCallback );
}

// keep a record of how many workers we created
gCurrentWorkers = global.config.get( 'NUM_WORKERS' );

// set a repeating 'tick' to perform the re-checks on down sites
setInterval( processQueuedRetries, global.config.get( 'TIME_BETWEEN_CHECKS_SEC' ) * SECONDS / 4 );

// start the 'recursive' stats logging
updateStats();
