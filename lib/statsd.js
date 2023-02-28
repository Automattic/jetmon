const os = require('os');

/**
 * Hostnames on prod look like:
 * 	<node>.<datacenter>.<domain>
 *
 * We only need the first 2 pieces and flip them around to make easier to group/filter things in StatsD later.
 * @type {string}
 */
const currentHostname = os.hostname().split( '.' ).slice( 0, 2 ).reverse().join( '.' );

/**
 * Set up the StatD client.
 *
 * All entries are prefixed with `com.jetpack.jetmon.<hostname>.`
 */
let statsdHostname = '127.0.0.1';

/**
 * Add a workaround for the local Docker instances, as prod is running statsd proxies on 127.0.0.1,
 * while the Docker nodes run it in the `statsd` container.
 */
if ( currentHostname === 'jetmon.docker' ) {
	statsdHostname = 'statsd';
}

const StatsD = require( 'hot-shots' );
const statsdClient = new StatsD( {
	host: statsdHostname,
	port: 8125,
	globalize: true,
	prefix: 'com.jetpack.jetmon.' + currentHostname + '.',
	errorHandler: function( error ) {
		console.log( "Socket errors caught here: ", error );
		logger.debug( 'Adding error log' + error );
	},
} );

module.exports = statsdClient;
