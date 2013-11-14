
const MEM_CONF_FILE = 'config/memcache.conf';

var fs     = require( 'fs' );
var mc     = require( 'memcached' );
var crc32  = require( 'buffer-crc32' );

var settings = new Object();

var memcache = {
	update : function( callBack ) {
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

	loadConfig : function( callBack ) {
		fs.readFile( MEM_CONF_FILE, function( err, data ) {
			if ( ! err && ( undefined != data ) ) {
				var aDataLines = data.toString().split( '\n' );
				var objectParsed = false;
				var strObject = "{ \"memcached_servers\" :{\n";

				for ( var loop = 0; loop < aDataLines.length; loop++ ) {
					if ( -1 != aDataLines[loop].indexOf( '//' ) ) {
						if ( 0 == aDataLines[loop].indexOf( '//' ) )
							continue;
						 else
							aDataLines[loop] = aDataLines[loop].substring( 0, aDataLines[loop].indexOf( '//' ) ) ;
					}
					if ( -1 != aDataLines[loop].indexOf( '$all_memcached_servers' ) ) {
						loop++;
						while ( ( loop < aDataLines.length ) && ( ! objectParsed ) ) {
							if ( aDataLines[loop].indexOf( "//" ) >= 0 ) {
								aDataLines[loop] = aDataLines[loop].substring( 0, aDataLines[loop].indexOf( '//' ) ) ;
							}
							if ( -1 != aDataLines[loop].indexOf( global.config.get( 'INSTALLED_DATACENTER' ) ) ) {
								strObject += "\t\"" + global.config.get( 'INSTALLED_DATACENTER' ) + "\" : {\n";
								loop++;
								var openBrackets = 1;
								while ( loop < aDataLines.length ) {
									if ( aDataLines[loop].indexOf( "//" ) >= 0 ) {
										aDataLines[loop] = aDataLines[loop].substring( 0, aDataLines[loop].indexOf( '//' ) ) ;
									}
									aDataLines[loop] = aDataLines[loop].replace( '=>', ':' );
									aDataLines[loop] = aDataLines[loop].replace( 'array', '' );
									while ( -1 != aDataLines[loop].indexOf( '(' ) ) {
										openBrackets++;
										aDataLines[loop] = aDataLines[loop].replace( '(', '[' );
									}
									while ( -1 != aDataLines[loop].indexOf( ')' ) ) {
										openBrackets--;
										aDataLines[loop] = aDataLines[loop].replace( ')', ']' );
									}

									if ( 0 == openBrackets ) {
										var strBefore = aDataLines[loop].substr( 0, aDataLines[loop].lastIndexOf( ']' ) );
										var strAfter = '';
										if ( aDataLines[loop].lastIndexOf( ']' ) < aDataLines[loop].length )
											strAfter = aDataLines[loop].substr( aDataLines[loop].lastIndexOf( ']' ) + 1, aDataLines[loop].length );
										strObject += strBefore + '}' + strAfter + "\n}\n}\n";
										strObject = strObject.replace( /'/g, '"' );
										strObject = strObject.replace(/,\ *\n*\t*\ *\]/g, '\n\t\t]');
										strObject = strObject.replace(/,\ *\n*\t*\ *\}/g, '\n\t}');
										objectParsed = true;
										loop = aDataLines.length;
									} else {
										strObject += aDataLines[ loop ] + "\n";
										loop++;
									}
								}
							} else {
								loop++;
							}
						}
					}
				}

				objectParsed = true;
				if ( objectParsed ) {
					settings = JSON.parse( strObject );
					callBack( true );
				} else {
					callBack( false );
				}
			} else {
				callBack( false );
			}
		});
	},

	getServerByKey : function( sKey, sGroup ) {
		var bucket = settings.memcached_servers[ global.config.get( 'INSTALLED_DATACENTER' ) ][sGroup];
		var hash = ( crc32.unsigned( sKey ) >> 16 ) & 0x7fff;
		return bucket[ ( hash ? hash : 1 ) % bucket.length ];
	},

	getUserCacheValue : function( userID, callBack ) {
		if ( undefined != settings.memcached_servers[ global.config.get( 'INSTALLED_DATACENTER' ) ] ) {
			var sServer = this.getServerByKey( userID, 'users' );

			var memcached = new mc( sServer, "{retries:2,retry:1000}");
			memcached.get( 'users:' + userID, function ( err, data ) {
				memcached.end();
				if ( err ) {
					console.log( "error retrieving cache: " + err );
					callBack( null );
				} else {
					console.log( "getUserCacheValue get data: " + data );
					callBack( data );
				}
			});
		} else {
			callBack( false );
		}
	},

	getUserMetaCacheValue : function( userID, callBack ) {
		if ( undefined != settings.memcached_servers[ global.config.get( 'INSTALLED_DATACENTER' ) ] ) {
			var sServer = this.getServerByKey( userID, 'default' );

			var memcached = new mc( sServer, "{retries:2,retry:1000}");
			memcached.get( 'user_meta:' + userID, function ( err, data ) {
				memcached.end();
				if ( err ) {
					console.log( "error retrieving cache: " + err );
					callBack( null );
				} else {
					console.log( "getUserMetaCacheValue get data: " + data );
					callBack( data );
				}
			});
		} else {
			callBack( false );
		}
	},

	getGlobalCacheValue : function( langID, callBack ) {
		if ( langID && ( undefined != settings.memcached_servers[ global.config.get( 'INSTALLED_DATACENTER' ) ] ) ) {
			var sServer = this.getServerByKey( langID, 'default' );

			var memcached = new mc( sServer, "{retries:2,retry:1000}");
			memcached.get( 'lang-code:' + langID, function ( err, data ) {
				memcached.end();
				if ( err ) {
					console.log( "error retrieving cache: " + err );
					callBack( null );
				} else {
					console.log( "getGlobalCacheValue get data: " + data );
					callBack( data );
				}
			});
		} else {
			callBack( false );
		}
	}
}

module.exports = memcache;
