
var fs    	   = require( 'fs' );
var templates  = require( './templates' );

var templates_cache = Array();

var templates = {
	render : function( templateFileName, templateKey, data ) {
		try {
			if ( ! templates_cache[ data.language ] ) {
				templates_cache[ data.language ] = JSON.parse( fs.readFileSync( global.config.get( 'templates' ).TEMPLATES_DIR + templateFileName ).toString() );
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
		}
		catch ( Exception ) {
			logger.error( 'error processing the template data for ' + templateFileName + ', ' + templateKey + ': ' + Exception.toString() );
			return null;
		}
	},
};

module.exports = templates;
