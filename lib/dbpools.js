
const DATASET      = 0;
const PARTITION    = 1;
const DATACENTER   = 2;
const READ_SLAVE   = 3;
const WRITE_MASTER = 4;
const INTERNET_URI = 5;
const INTERNAL_URI = 6;
const DB_NAME      = 7;
const DB_USER      = 8;
const DB_PASSWORD  = 9;

const DB_CONF_FILE = 'config/db-config.conf';

var fs    = require( 'fs' );
var mysql = require( 'mysql' );

var poolCluster = mysql.createPoolCluster();

poolCluster.on( 'remove', function( nodeName ) {
	logger.debug( 'MySQL node has been removed : ' + nodeName );
	// TODO: check config for updates and reload connections if necessary
});

poolCluster.on( 'error', function( err ) {
	logger.debug( "pool cluster error:" + err );
	// TODO: check error and reload connections if necessary
});

var configuration = {
	update : function ( callBack ) {
		var execute = require('child_process').exec;
		var result = execute( "/usr/local/bin/jetmon-config-update.sh",
								function( error, stdout, stderr ) {
									if ( null !== error ) {
										logger.debug( 'error updating the config: ' + error );
										callBack( false );
									} else {
										if ( 'OK' === stdout )
											callBack( true );
										else
											callBack( false );
									}
								} );
	},

	load : function( callBack ) {
		fs.readFile( DB_CONF_FILE, function( err, data ) {
			if ( ! err && ( undefined != data ) ) {
				var aDataLines = data.toString().split( '\n' );
				var sSlaveCount = 1;
				var sBackupCount = 1;
				for ( var loop = 0; loop < aDataLines.length; loop++ ) {
					if ( ( -1 != aDataLines[loop].indexOf( 'add_db_server' ) ) && ( -1 != aDataLines[loop].indexOf( "'misc'" ) ) &&
						( -1 != aDataLines[loop].indexOf( '(' ) ) && ( -1 != aDataLines[loop].indexOf( ')' ) ) ) {

						parsed = aDataLines[loop].substr( aDataLines[loop].indexOf( "'" ), aDataLines[loop].lastIndexOf( ')' ) );
						arrSettings = parsed.split( ',' );

						if ( 13 == arrSettings.length ) {
							if ( -1 != arrSettings[ DATASET ].indexOf( "misc" ) ) {
								for ( var cleanloop = 0; cleanloop < 13; cleanloop++ )
									arrSettings[cleanloop] = arrSettings[cleanloop].replace(/\'/g, '' ).replace(/\ /g, '' );

								var db_config = {
												host     : arrSettings[ INTERNAL_URI ].split( ':' )[0],
												port     : arrSettings[ INTERNAL_URI ].split( ':' )[1],
												user     : arrSettings[ DB_USER ],
												password : arrSettings[ DB_PASSWORD ],
												database : arrSettings[ DB_NAME ],
											};
								if ( 1 == arrSettings[ WRITE_MASTER ] ) {
									db_config['multipleStatements'] = true;
									poolCluster.add( 'MASTER', db_config );
								} else {
									if ( -1 != arrSettings[ DATACENTER ].indexOf( "dfw" ) )
										poolCluster.add( 'SLAVE' + sSlaveCount++, db_config );
									else if ( -1 == arrSettings[ DATACENTER ].indexOf( "'bak'" ) ) // ignore backup servers
										poolCluster.add( 'FAILOVER' + sBackupCount++, db_config );
								}
							}
						}
					}
				}
				callBack( true );
			} else {
				callBack( false );
			}
		});
	}
}

exports.cluster = poolCluster;
exports.config  = configuration;
