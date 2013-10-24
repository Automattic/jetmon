
process.title = 'jetmon.js';

var config  = require( './config' );
var cluster = require( 'cluster' );

var jetmonMysql = require( './jetmonmysql' );
var jetmonMailer  = require( './jetmonmailer' );

console.log( 'booting jetmon.js' );

process.on( 'SIGINT', printTotalsExit );
process.on( 'EXIT',   printTotalsExit );

process.on( 'uncaughtException', function( errDesc ) {
	console.log( 'uncaughtException error: ' + errDesc );
});

cluster.setupMaster( {
	exec   : './lib/httpcheck.js',
	silent : false
});

cluster.on( 'exit', function( worker, code, signal ) {
    console.log( 'A worker died:', worker, code, signal);
});

function printTotalsExit() {
    printTotals();
    if ( config.sendmails )
    	jetmonMailer.exit();
	process.exit( 0 );
}

var gCountSuccess = 0;
var gCountError 	= 0;
var startTime		= new Date().getTime();
var sitesCount		= 0;

function printTotals() {
    console.log( '' );
    console.log( 'Error: ' + gCountError );
    console.log( 'Success: ' + gCountSuccess );
    console.log( 'Total: ' + (gCountSuccess + gCountError) );
    var now = new Date().getTime();
    console.log( 'Time: ' + Math.floor( (now - startTime) / 60000 ) + 'm '
        + ( ( (now - startTime) % 60000 ) / 1000 ) + 's');
}

var arrObjects    = new Array();
var freeWorkers   = new Array();
var queuedRetries = new Array();
var gettingSites  = false;

var endOfRound = false;

function resetEnv() {
    fromBucketNo = config.BUCKET_NO_MIN;
    toBucketNo = fromBucketNo + config.BATCH_SIZE;
    gCountSuccess = 0;
    gCountError = 0;
    startTime		= new Date().getTime();
    endOfRound = false;
}

function getMoreSites() {
    gettingSites = true;
    if ( endOfRound ) {
        var timeToNextLoop = config.MIN_TIME_BETWEEN_ROUNDS - ( new Date().getTime() - startTime );
        if ( config.DEBUG === true ) {
            printTotals();
            console.log('Next round will start in: ',timeToNextLoop);
        }
        setTimeout(
            function() {
                resetEnv();
                getMoreSites();
            },
            timeToNextLoop
        );
        return;
    }
    endOfRound = jetmonMysql.getNextBatch(
        function( rows ) {
            for ( var i = 0; i < rows.length; i++ ) {
                var server = rows[i];
                server.processed = false;
                server.checked = 0;
                server.rtt = 0;
                server.oldStatus = server.site_status;
                arrObjects.push( server );
            }
            gettingSites = false;
            freeWorkersToWork();
        }
    );
}

function freeWorkersToWork() {
    for ( var i = 0; i < freeWorkers.length; i++ )
        workerMsgCallback( { msgtype: 'i_need_some_work', workerid: freeWorkers.shift() } );
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
            case 'notify':
                jetmonMysql.saveNewStatus( msg.server );
                if ( config.sendmails ) {
                    jetmonMailer.sendMail( msg.server );
                }
                break;
            case 'i_need_some_work':
                if ( arrObjects.length == 0 ) {
                    freeWorkers.push( msg.workerid );
                    if ( ! gettingSites ) {
                        gettingSites = true;
                        getMoreSites();
                    }
                    break;
                }
                //send some work
                cluster.workers[msg.workerid].send( { id: msg.workerid, request: 'queue-add', payload: arrObjects.splice( 0, config.BUCKET_SIZE ) } );
                break;
            case 'recheck':
                msg.server.processed = false;
                queuedRetries.push( msg.server );
   			default:
		}
	}
	catch ( Exception ) {
		console.log( "Error receiving worker's message: ", Exception, msg );
	}
}

function workersCount() {
    var workersCount = 0;
    for ( worker in cluster.workers )
        workersCount ++;
    return workersCount;
}

function updateSitesPerSec() {
	console.log( 'sites/sec = ' + sitesCount + ' - ' + workersCount() + '/' + config.NUM_WORKERS + ' working' );
	sitesCount = 0;
    if ( 0 == cluster.workers.length )
    	printTotalsExit();
    else
    	setTimeout( updateSitesPerSec, 1000 );
}

resetEnv();

// Create all our workers and keep an array of them to allow communication back and forth

for ( var i = 0; i < config.NUM_WORKERS; i++ ) {
	var worker = cluster.fork();
	worker.on( 'message', workerMsgCallback );
}

function processQueuedRetries() {
    for( var i = 0; i < queuedRetries.length; i++ ){
        var server = queuedRetries.shift();
        var now = new Date().getTime();
        if ( now > server.lastCheck + config.TIME_BETWEEN_CHECKS ) {
            arrObjects.push( server );
        } else {
            queuedRetries.push( server );
        }
    }
    freeWorkersToWork();
}

setInterval(
    processQueuedRetries,
    config.TIME_BETWEEN_CHECKS / 4
);

if ( config.DEBUG === true )
    updateSitesPerSec();
