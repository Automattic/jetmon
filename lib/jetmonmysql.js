var config  = require( './config' );
var mysql   = require( 'mysql'    );

var write_pool = mysql.createPool({
    host     : config.mysql.write.host,
    port     : config.mysql.write.port,
    user     : config.mysql.write.user,
    password : config.mysql.write.password,
    database : config.mysql.write.database,
});

var read_pool = mysql.createPool({
    host     : config.mysql.read.host,
    port     : config.mysql.read.port,
    user     : config.mysql.read.user,
    password : config.mysql.read.password,
    database : config.mysql.read.database,
});

var from_bucket_no = 0 - config.batch_size;
var to_bucket_no = 0;

var jetmonmysql = {
        get_next_batch : function( after ) {

            from_bucket_no = from_bucket_no + config.batch_size;
            if ( from_bucket_no >= config.bucket_no_max ) {
                from_bucket_no = 0;
            }
            to_bucket_no = from_bucket_no + config.batch_size;
            if ( to_bucket_no > config.bucket_no_max ) {
                to_bucket_no = config.bucket_no_max;
            }

            read_pool.getConnection( function( err, connection ) {

                var query = 'SELECT * FROM jetpack_monitor_subscription WHERE bucket_no >= '
                    + from_bucket_no + ' AND bucket_no < ' + to_bucket_no + ' AND monitor_active = 1'
                console.log(query);
                connection.query(
                    query,
                    function(err, rows) {
                        // And done with the connection.
                        connection.release();
                        after( rows );
                    }
                );
            });
            // return true if it is the last batch.
            return to_bucket_no == config.bucket_no_max;
        },
        save_new_status : function ( server ) {
            write_pool.getConnection( function( err, connection ) {
                var query = 'UPDATE jetpack_monitor_subscription SET site_status = ' + server.site_status + ', last_status_change = NOW() WHERE blog_id=' + server.blog_id;
                console.log( query );
                connection.query(
                    query,
                    function( err, rows ) {
                        connection.release();
                    }
                );
            });
        }
};


module.exports = jetmonmysql;