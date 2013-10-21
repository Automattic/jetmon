
process.title = 'jetmon.js';

var config  = require( './config' );
var cluster = require( 'cluster' );
var jetmonmailer  = require( "./jetmonmailer" );
var jetmonmysql = require( "./jetmonmysql" );

var g_count_success = 0;
var g_count_error 	= 0;
var start_time		= new Date().getTime();

var sites_count		= 0;
var lastLoop 		= new Date().getTime();

var numWorkers 		= config.numWorkers;


console.log( 'booting jetmon.js' );

process.on( 'SIGINT', print_totals_exit );
process.on( 'EXIT',   print_totals_exit );

process.on( 'uncaughtException', function( err_desc ) {
	console.log( 'uncaughtException error: ' + err_desc );
});

cluster.setupMaster( {
	exec   : './lib/httpcheck.js',
	silent : false
});

cluster.on( 'exit', function( worker, code, signal ) {
    console.log(worker, code, signal);
});

function print_totals_exit() {
    print_totals();
    if ( config.sendmails )
        smtpTransport.close(); // shut down the smtp connection pool
	process.exit( 0 );
}

function print_totals() {
    console.log( '' );
    console.log( 'Error: ' + g_count_error );
    console.log( 'Success: ' + g_count_success );
    console.log( 'Total: ' + (g_count_success + g_count_error) );
    var now = new Date().getTime();
    console.log( 'Time: ' + Math.floor( (now - start_time) / 60000 ) + 'm '
        + ( ( (now - start_time) % 60000 ) / 1000 ) + 's');
}

var bucket_size;

var arrObjects = new Array();
var freeWorkers = new Array();
var getting_sites = false;

var end_of_round = false;

function reset_env() {
    from_bucket_no = config.bucket_no_min;
    to_bucket_no = from_bucket_no + config.batch_size;
    g_count_success = 0;
    g_count_error = 0;
    start_time		= new Date().getTime();
    end_of_round = false;
}

function get_more_sites() {
    getting_sites = true;
    if ( end_of_round ) {
        print_totals();
        var time_to_next_loop = config.MIN_TIME_BETWEEN_ROUNDS - ( new Date().getTime() - start_time );
        console.log('Next round will start in: ', time_to_next_loop);
        setTimeout(
            function() {
                reset_env();
                get_more_sites();
            },
            time_to_next_loop
        );
        return;
    }
    end_of_round = jetmonmysql.get_next_batch(
        function( rows ) {
            for ( var i = 0; i < rows.length; i++ ) {
                var server = rows[i];
                server.processed = false;
                server.checked = 0;
                server.rtt = 0;
                server.last_check = 0;
                arrObjects.push( server );
            }
            bucket_size    =  Math.ceil( ( arrObjects.length ) / numWorkers);
            getting_sites = false;
            freeWorkers_to_work()
        }
    );
}

function freeWorkers_to_work() {
    for( var id in freeWorkers ) {
        worker_msg_callback( { msgtype: 'i_need_some_work', workerid: freeWorkers.pop() } );
    }
}

function worker_msg_callback( msg ) {
    try {

		switch ( msg.msgtype ) {
            case "totals":
                if ( msg.server.site_status )
                    g_count_success ++
                else
                    g_count_error ++
                sites_count++;
                break;
            case "notify":
                jetmonmysql.save_new_status( msg.server );
                if ( config.sendmails ) {
                    jetmonmailer.send_mail( msg.server );
                }
                break;
            case "i_need_some_work":
                if ( arrObjects.length == 0 ) {
                    freeWorkers.push( msg.workerid );
                    if ( ! getting_sites ) {
                        getting_sites = true;
                        get_more_sites();
                    }
                    break;
                }
                //send some work
                cluster.workers[msg.workerid].send( { id: msg.workerid, request: 'queue-add', payload: arrObjects.splice( 0, bucket_size ) } );
                break;
            case "recheck":
                msg.server.processed = false;
                setTimeout(
                    function() {
                        arrObjects.push( msg.server );
                        freeWorkers_to_work();
                    },
                    config.TIME_BETWEEN_CHECKS
                );
   			default:
		}
	}
	catch ( Exception ) {
		console.log( "Error receiving worker's message: ", Exception, msg );
	}
}


function update_sites_per_sec() {
    var workers_count = 0;
    for ( worker in cluster.workers )
        workers_count ++;
	console.log( 'sites/sec = ' + sites_count + ' - ' + workers_count + '/' + numWorkers + ' working' );
	sites_count = 0;
    lastLoop = new Date().getTime();
    if ( 0 == cluster.workers.length )
    	print_totals_exit();
    else
    	setTimeout( update_sites_per_sec, 1000 );
}

reset_env();

// Create all our workers and keep an array of them to allow communication back and forth

for (var i = 0; i < numWorkers; i++) {
	var worker = cluster.fork();
	worker.on( 'message', worker_msg_callback );
}

update_sites_per_sec();
