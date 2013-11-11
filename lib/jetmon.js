
process.title = 'jetmon-master';

var config   = require( './config' );
var cluster  = require( 'cluster' );
var fs       = require( 'fs' );
var o_log4js = require( 'log4js' );

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

var db_mysql = require( './database' );
var postman  = require( './mailer' );

var gCountSuccess = 0;
var gCountError   = 0;
var startTime     = new Date().valueOf();
var sitesCount    = 0;
var arrObjects    = new Array();
var freeWorkers   = new Array();
var queuedRetries = new Array();
var gettingSites  = false;
var endOfRound    = false;

const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

logger.debug( 'booting jetmon.js' );

process.on( 'SIGINT', gracefulShutdown );
process.on( 'EXIT', gracefulShutdown );
process.on( 'SIGHUP', gracefulShutdown );

process.on( 'uncaughtException', function( errDesc ) {
	logger.debug( 'uncaughtException error: ' + errDesc );
});

cluster.setupMaster( {
	exec   : './lib/httpcheck.js',
	silent : false
});

cluster.on( 'exit', function( worker, code, signal ) {
	logger.debug( 'A worker died:', worker, code, signal );
});

function stopWorkers() {
	console.log( 'Caught shutdown signal, disconnecting worker threads.' );
	for ( var workerid in cluster.workers )
		cluster.workers[ workerid ].disconnect();
}

function gracefulShutdown() {
	// Note: calling the 'logger' object during shutdown causes an immediate exit (only use 'console.log')
	stopWorkers();
	db_mysql.commitUpdates();
	console.log( 'committed any outstanding db updates.' );
	printTotalsExit();
	process.exit( 0 );
}

function printTotalsExit() {
	printTotals();
	if ( config.sendmails )
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
}

function getMoreSites() {
	gettingSites = true;
	if ( endOfRound ) {
		var timeToNextLoop = config.MIN_TIME_BETWEEN_ROUNDS - ( new Date().valueOf() - startTime );

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
				if ( config.sendmails ) {
					postman.sendStatusChangeMail( msg.server );
				}
				break;
			case 'notify_still_down':
				if ( config.sendmails ) {
					msg.server.site_status = SITE_CONFIRMED_DOWN;
					db_mysql.saveNewStatus( msg.server );
					postman.sendStillDownMail( msg.server );
				}
				break;
			case 'send_work':
				if ( true === config.DEBUG )
					logger.debug( 'send_work: ' + msg.workerid + ', have ' + arrObjects.length + ' items queued' );
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
															payload : arrObjects.splice( 0, Math.min( arrObjects.length, config.DATASET_SIZE ) )
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
		if ( true === config.DEBUG ) {
			var timeToNextLoop = config.MIN_TIME_BETWEEN_ROUNDS - ( new Date().valueOf() - startTime );
			logger.debug( 'sps = ' + sitesCount + ' - ' + ( config.NUM_WORKERS - freeWorkers.length ) + ' working, ' +
							freeWorkers.length + ' waiting : next round in ' + ( timeToNextLoop / 1000 ) + 's' );
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
			totalFile.write( 'working : ' + ( config.NUM_WORKERS - freeWorkers.length ) + '\n' );
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
		setTimeout( updateStats, ( config.STATS_UPDATE_INTERVAL ) );
	}
}

function processQueuedRetries() {
	if ( true === config.DEBUG )
		logger.debug( 'starting checks for ' + queuedRetries.length + ' queued retries' );
	var tmpRetries = queuedRetries; // take pointer
	queuedRetries = [];				// and reset
	for( var loop = 0; loop < tmpRetries.length; loop++ ) {
		if ( new Date().valueOf() > ( tmpRetries[loop].lastCheck + config.TIME_BETWEEN_CHECKS ) ) {
			arrObjects.push( tmpRetries[loop] );
		}
	}
	freeWorkersToWork();
}

// Create all our workers and keep an array of them to allow communication back and forth
for ( var i = 0; i < config.NUM_WORKERS; i++ ) {
	var worker = cluster.fork();
	worker.on( 'message', workerMsgCallback );
}

// set a repeating 'tick' to perform the re-checks on down sites
setInterval( processQueuedRetries, config.TIME_BETWEEN_CHECKS / 4 );

// start the 'recursive' stats logging
updateStats();
