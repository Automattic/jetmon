
var config     = require( './config' );
var fs    	   = require( 'fs' );
var templates  = require( './templates' );

var templates_cache = Array();

var templates = {
	render : function ( templateFileName, templateKey, data ) {
		if ( ! templates_cache[ data.language ] ) {
			templates_cache[ data.language ] = JSON.parse( fs.readFileSync( config.templates.TEMPLATES_DIR + templateFileName ).toString() );
		}
		if ( Object.prototype.toString.call( templates_cache[ data.language ][ templateKey ] ) === '[object Array]'  ) {
			templates_cache[ data.language ][ templateKey ] = templates_cache[ data.language ][ templateKey ].join('\n');
		}
		if ( ! templates_cache[ data.language ][ templateKey ] ) {
			logger.error( 'Error loading template:', data.language, templateKey );
			return false;
		}
		return templates_cache[ data.language ][ templateKey ].replace(
			/%(\w*)%/g, 
			function( m, key ) {
				return data.hasOwnProperty( key ) ? data[ key ] : '';
			}
		);
	},
};

module.exports = templates;
