
var fs = require( 'fs' );

var templatesCache = Array();

var templates = {

	renderHTML : function( templateFileName, templateBaseFileName, templateKey, data ) {
		try {						
			if ( ! templatesCache[ templateBaseFileName ] ) {
				templatesCache[ templateBaseFileName ] = fs.readFileSync( global.config.get( 'templates' ).TEMPLATES_DIR + templateBaseFileName, 'utf8' );
			}

			var htmlData = {
				title : this.render( templateFileName, templateKey + 'HtmlTitle', data ),
				content : this.render( templateFileName, templateKey + 'HtmlContent', data ),
				footer : this.render( templateFileName, 'HtmlFooter', data ),
				footer_notice : this.render( templateFileName, 'HtmlFooterNotice', data ),
			}

			return this.templateReplace( templatesCache[ templateBaseFileName ], htmlData );
		}
		catch ( Exception ) {
			logger.error( 'error rendering HTML template data for ' + templateFileName + ', ' + templateKey + ': ' + Exception.toString() );
			return null;
		}
	},

	render : function( templateFileName, templateKey, data ) {
		try {
			templates = this.filenameToTemplateObject( templateFileName );
			if ( ! templates[ templateKey ] ) {
				logger.error( 'Error loading template:', templateFileName, templateKey );
				return false;
			}
			return this.templateReplace( templates[ templateKey ], data );
		}
		catch ( Exception ) {
			logger.error( 'error processing the template data for ' + templateFileName + ', ' + templateKey + ': ' + Exception.toString() );
			return null;
		}
	},

	templateReplace : function( templateString, data ) {
		return templateString.replace(
			/%(\w*)%/g,
			function( m, key ) {
				return data.hasOwnProperty( key ) ? data[ key ] : '';
			}
		);
	},

	filenameToTemplateObject : function( templateFileName ) {
		if ( ! templatesCache[ templateFileName ] ) {
			templatesCache[ templateFileName ] = JSON.parse( fs.readFileSync( global.config.get( 'templates' ).TEMPLATES_DIR + templateFileName ).toString() );
			for ( var property in templatesCache[ templateFileName ] ) {
				if ( Object.prototype.toString.call( templatesCache[ templateFileName ][ property ] ) === '[object Array]'  ) {
					templatesCache[ templateFileName ][ property ] = templatesCache[ templateFileName ][ property ].join('\n');
				}			
			}
		}
		return templatesCache[ templateFileName ];
	},
	
	emptyCache : function() {
		templatesCache = Array();
	}

};

module.exports = templates;
