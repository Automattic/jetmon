
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

var fs     = require( 'fs' );
var mysql  = require( 'mysql' );

var poolCluster = mysql.createPoolCluster();

poolCluster.on( 'remove', function( nodeName ) {
	logger.debug( 'node has been removed : ' + nodeName );
});

poolCluster.on( 'error', function( err ) {
	logger.debug( "pool cluster error:" + err );
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
			if ( err || ( undefined === data ) ) {
				logger.error( 'error loading the db config file: ' + err );
				callBack( false );
			}
			var aDataLines = data.toString().split( '\n' );
			var slaveUniqueCount = 1;
			var backupUniqueCount = 1;
			for ( var loop = 0; loop < aDataLines.length; loop++ ) {
				if ( -1 != aDataLines[loop].indexOf( '//' ) ) {
					if ( 0 == aDataLines[loop].indexOf( '//' ) )
						continue;
					 else
						aDataLines[loop] = aDataLines[loop].substring( 0, aDataLines[loop].indexOf( '//' ) ) ;
				}
				if ( ( -1 != aDataLines[loop].indexOf( 'add_db_server' ) ) &&
					( -1 != aDataLines[loop].indexOf( '(' ) ) &&
					( -1 != aDataLines[loop].indexOf( ')' ) ) ) {

					var parsed = aDataLines[loop].substr( aDataLines[loop].indexOf( "'" ), aDataLines[loop].lastIndexOf( ')' ) );
					arrSettings = parsed.split( ',' );

					if ( 13 == arrSettings.length ) {
						for ( var cleanloop = 0; cleanloop < 13; cleanloop++ )
							arrSettings[cleanloop] = arrSettings[cleanloop].replace(/\'/g, '' ).replace(/\ /g, '' );

						var db_config = {
										host              : arrSettings[ INTERNAL_URI ].split( ':' )[0],
										port              : arrSettings[ INTERNAL_URI ].split( ':' )[1],
										user              : arrSettings[ DB_USER ],
										password          : arrSettings[ DB_PASSWORD ],
										database          : arrSettings[ DB_NAME ],
										connectionLimit   : 5,
										supportBigNumbers : true,
									};

						if ( 1 == arrSettings[ WRITE_MASTER ] ) {
							// only misc master allowed
							if ( -1 != arrSettings[ DATASET ].indexOf( "misc" ) ) {
								db_config['multipleStatements'] = true;
								poolCluster.add( 'MISC_MASTER', db_config );
							}
						} else {
							var poolPrefix = '';
							if ( -1 != arrSettings[ DATASET ].indexOf( "misc" ) )
								poolPrefix = 'MISC_';
							else if ( -1 != arrSettings[ DATASET ].indexOf( "global" ) )
								poolPrefix = 'GLOBAL_';
							else if ( -1 != arrSettings[ DATASET ].indexOf( "user" ) )
								poolPrefix = 'USER_';
							else
								continue; // dataset not required

							if ( -1 != arrSettings[ DATACENTER ].indexOf( global.config.get( 'INSTALLED_DATACENTER' ) ) ) {
								poolCluster.add( poolPrefix + 'SLAVE' + slaveUniqueCount++, db_config );
							} else if ( -1 == arrSettings[ DATACENTER ].indexOf( "'bak'" ) ) {
								// change to external URI's for non-local DC servers
								db_config.host = arrSettings[ INTERNET_URI ].split( ':' )[0];
								db_config.port = arrSettings[ INTERNET_URI ].split( ':' )[1];
								poolCluster.add( poolPrefix + 'FAILOVER' + backupUniqueCount++, db_config );
							}
						}
					}
				}
			}
			callBack( true );
		});
	}
}

exports.cluster = poolCluster;
exports.config  = configuration;
