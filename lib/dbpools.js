
const DATACENTER   = 0;
const READ_SLAVE   = 1;
const WRITE_MASTER = 2;
const INTERNET_URI = 3;
const INTERNAL_URI = 4;
const DB_NAME      = 5;
const DB_USER      = 6;
const DB_PASSWORD  = 7;

const DB_CONF_FILE     = 'config/db-config.conf';
const DB_ORIGINAL_FILE = 'config/db-config_original.conf';
const DB_UPDATE_SCRIPT = '/usr/local/bin/jetmon-config-update.sh';

var fs     = require( 'fs' );
var mysql  = require( 'mysql' );

var reloadConfig = false;
var poolCluster = mysql.createPoolCluster();

poolCluster.on( 'remove', function( nodeName ) {
	logger.debug( 'node has been removed : ' + nodeName );
});

poolCluster.on( 'error', function( err ) {
	logger.error( "pool cluster error:" + err );
});

var configuration = {
	reload : function() {
		logger.debug( 'reloading the DB config' );
		poolCluster = mysql.createPoolCluster();
		if ( configuration.load() )
			logger.debug( 'DB config has been reloaded' );
		else
			logger.error( 'DB config failed to reload' );
	},

	update : function( callBack ) {
		var execute = require('child_process').exec;
		fs.stat( DB_ORIGINAL_FILE, function( err, stats ) {
			if ( err ) {
				logger.error( 'stat error on the config file: ' + err );
				callBack( false );
				return;
			}
			var mDate = stats.mtime.valueOf();
			var result = execute( DB_UPDATE_SCRIPT,
								function( error, stdout, stderr ) {
									if ( error ) {
										logger.error( 'error updating the config: ' + error );
										callBack( false );
									} else {
										if ( 0 === stdout.length ) {
											fs.stat( DB_ORIGINAL_FILE, function( err, stats ) {
												if ( err ) {
													logger.error( 'stat error on the config file: ' + error );
													callBack( false );
												} else {
													if ( stats.mtime.valueOf() > mDate )
														callBack( true );
													else
														callBack( false );
												}
											});
										} else {
											callBack( false );
										}
									}
								});
		});
	},

    load: function( callBack ) {
          var data = fs.readFileSync( DB_CONF_FILE );
          if ( undefined === data ) {
              logger.error( 'error loading the db config file: ' + err );
              if ( undefined !== callBack ) {
                  callBack( false );
                  return;
              } else {
                  return false;
              }
          }
          var aDataLines = data.toString().split( '\n' );
          var slaveUniqueCount = 1;
          var backupUniqueCount = 1;
          var currentDataset;
          var datasetPattern = /^\s'([-\w]+)'\s*=>\s*array\(/;
          var serverPattern = /^\s*array\(/;

          for ( var loop = 0; loop < aDataLines.length; loop++ ) {
              var match = datasetPattern.exec( aDataLines[loop] );
              if ( match )
                  currentDataset = match[1];

              if ( 'misc' !== currentDataset )
                  continue;

              if ( ! serverPattern.test( aDataLines[loop] ) )
                  continue;

              var arrSettings = aDataLines[loop].substr( aDataLines[loop].indexOf( "'" ), aDataLines[loop].lastIndexOf( ')' ) - aDataLines[loop].indexOf( "'" ) );
              arrSettings = arrSettings.replace(/\'/g, '' ).replace(/\"/g, '' ).replace(/ /g, '' ).split( ',' );
              if ( 11 != arrSettings.length )
                  continue;

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
                  db_config['multipleStatements'] = true;
                  poolCluster.add( 'MISC_MASTER', db_config );
              } else {
                  var poolPrefix = 'MISC_';

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

          if ( undefined !== callBack )
              callBack( true );
          else
              return true;
    }
}

exports.cluster = poolCluster;
exports.config  = configuration;
