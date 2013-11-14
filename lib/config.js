
const CONFIG_FILE = 'config/config.json';

var fs = require( 'fs' );

var config = {
	_cache: null,
	load: function( s_file_name ) {
		this._cache = JSON.parse( fs.readFileSync( CONFIG_FILE ).toString() );
		return this._cache;
	},
	get: function( key ) {
		return this._cache[ key ];
	}
};

module.exports = config;
