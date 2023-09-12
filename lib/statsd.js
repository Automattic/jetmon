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
 * The MTU of the network connection that sends StatsD metrics is used to
 * determine the max buffer size.
 */
let statsdMTU = 65536;

/**
 * The number of milliseconds that can elapse before buffered StatsD metrics are
 * flushed.
 */
let statsdFlushInterval = 5000;

/**
 * Add a workaround for the local Docker instances, as prod is running statsd proxies on 127.0.0.1,
 * while the Docker nodes run it in the `statsd` container.
 */
if ( currentHostname === 'jetmon.docker' ) {
	statsdHostname = 'statsd';
	statsdMTU = 1500;
}

const prefix = 'com.jetpack.jetmon.' + currentHostname + '.';


const statsdClient = {
	init: function( prefix, host, port, mtu, flushInterval, logger ) {
		this.prefix        = prefix;
		this.host          = host;
		this.port          = port;
		this.maxBufferSize = mtu - 29; // Reduce by 29 to account for packet headers.
		this.logger        = logger;

		this.buffer = '';
		this.enabled = global.config.get( 'STATSD_ENABLE', true );

		if ( this.enabled ) {
			this.socket = dgram.createSocket( 'udp4' );
			this.socket.on( 'error', (error) => this.logger.error( error ) );

			this.interval = setInterval( () => {
				this.flush();
			}, flushInterval );
		}
	},

	increment: function( metric, value = 1, sampleRate = 1) {
		this.send( `${metric}:${value}|c|@${sampleRate}` );
	},

	timing: function( metric, value, sampleRate = 1 ) {
		this.send( `${metric}:${value}|ms|@${sampleRate}` );
	},

	gauge: function( metric, value ) {
		this.send( `${metric}:${value}|g` );
	},

	send: function( message ) {
		if ( this.enabled ) {
			message = `${this.prefix}${message}\n`;

			// If the total buffer size is already at the maximum size, flush it first
			if ( this.buffer.length + message.length >= this.maxBufferSize ) {
				this.flush();
			}

			// Append the message to the buffer
			this.buffer += message;
		}
	},

	flush: function() {
		if ( this.enabled && this.buffer.length > 0 ) {
			const buffer = this.buffer;
			this.buffer = '';
			try {
				this.socket.send( buffer, this.port, this.host, (error) => {
					if ( error ) {
						this.logger.error( 'Error when sending to statsd: ' + error.toString() );
					}
				});
			}
			catch ( Exception ) {
				this.logger.error( 'Exception when sending to statsd: ' + Exception.toString() );
			}
		}
	},

	close: function() {
		if ( this.enabled ) {
			clearInterval( this.interval );
			this.socket.close();
		}
	}
}

statsdClient.init( prefix, statsdHostname, 8125, statsdMTU, statsdFlushInterval, logger );

module.exports = statsdClient;
