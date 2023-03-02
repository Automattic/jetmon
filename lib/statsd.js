const os    = require('os');
const dgram = require('dgram');

/**
 * Hostnames on prod look like:
 *  <node>.<datacenter>.<domain>
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

const prefix = 'com.jetpack.jetmon.' + currentHostname + '.';

/**
 * Start as null as the send function will recreate the variable as needed.
 */
let socket = null;

const statsdClient = {
	increment: function( metric, value ) {
		if ( typeof value === 'undefined' ) {
			value = 1;
		}
		statsdClient.send( metric, value, 'c' );
	},

	timing: function( metric, time ) {
		statsdClient.send( metric, time, 'ms' );
	},

	send: function( metric, value, type ) {
		let message = prefix + metric + ':' + value + '|' + type;

		try {
			if ( ! socket ) {
				socket = dgram.createSocket( 'udp4' );
			}
			socket.send( message, 8125, statsdHostname, (err) => {
				if ( err ) {
					logger.error( 'Error when sending to statsd: ' + err.toString() );
				}
			});
		}
		catch ( Exception ) {
			logger.error( 'Exception when sending to statsd: ' + Exception.toString() );
		}
	}
}

module.exports = statsdClient;
