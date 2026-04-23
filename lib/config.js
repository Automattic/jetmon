
const CONFIG_FILE = 'config/config.json';

var fs = require( 'fs' );

var config = {
	_cache: null,
	load: function( s_file_name ) {
		this._cache = JSON.parse( fs.readFileSync( CONFIG_FILE ).toString() );
		return this._cache;
	},
	get: function( key, default_value ) {
		if ( 'undefined' !== typeof this._cache[ key ] ) {
			return this._cache[ key ];
		} else if ( 'undefined' !== typeof default_value ) {
			console.log( {key: key, value: default_value, source: 'default'} );
			return default_value;
		}

		console.log( {key: key, value: null, source: 'null'} );
		return null;
	}
};

module.exports = config;
