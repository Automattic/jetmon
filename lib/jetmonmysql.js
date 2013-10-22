var config  = require( './config' );
var mysql   = require( 'mysql'    );

var write_pool = mysql.createPool({
    host     : config.mysql.write.HOST,
    port     : config.mysql.write.PORT,
    user     : config.mysql.write.USER,
    password : config.mysql.write.PASSWORD,
    database : config.mysql.write.DATABASE,
});

var read_pool = mysql.createPool({
    host     : config.mysql.read.HOST,
    port     : config.mysql.read.PORT,
    user     : config.mysql.read.USER,
    password : config.mysql.read.PASSWORD,
    database : config.mysql.read.DATABASE,
});

var fromBucketNo = 0 - config.BATCH_SIZE;
var toBucketNo = 0;

var jetmonMysql = {
    getNextBatch : function( afterQueryFunction ) {

        fromBucketNo = fromBucketNo + config.BATCH_SIZE;
        if ( fromBucketNo >= config.BUCKET_NO_MAX ) {
            fromBucketNo = 0;
        }
        toBucketNo = fromBucketNo + config.BATCH_SIZE;
        if ( toBucketNo > config.BUCKET_NO_MAX ) {
            toBucketNo = config.BUCKET_NO_MAX;
        }

        read_pool.getConnection( function( err, connection ) {

            var query = 'SELECT * FROM jetpack_monitor_subscription WHERE bucket_no >= '
                + fromBucketNo + ' AND bucket_no < ' + toBucketNo + ' AND monitor_active = 1'
            if ( config.DEBUG === true )
                console.log(query);
            connection.query(
                query,
                function(err, rows) {
                    // And done with the connection.
                    connection.release();
                    afterQueryFunction( rows );
                }
            );
        });
        // return true if it is the last batch.
        return toBucketNo == config.BUCKET_NO_MAX;
    },
    saveNewStatus : function ( server ) {
        write_pool.getConnection( function( err, connection ) {
            var query = 'UPDATE jetpack_monitor_subscription SET site_status = ' + server.site_status + ', last_status_change = NOW() WHERE blog_id=' + server.blog_id;
            if ( config.DEBUG === true )
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


module.exports = jetmonMysql;
