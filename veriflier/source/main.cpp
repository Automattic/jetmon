
#include <QCoreApplication>

#include "headers/config.h"
#include "headers/logger.h"
#include "headers/ssl_server.h"
#include "headers/bot_auth.h"

#include <iostream>

int main( int argc, char *argv[] )
{
	QCoreApplication app(argc, argv);
	Logger::instance()->startLogger();

	SSL_Server *ssl = new SSL_Server();
	bool result = ssl->listen( QHostAddress::Any, Config::instance()->get_int_value( "listen_port" ) );

	if ( ! result ) {
		LOG( "failed to open the server port, eXiting." );
		Logger::instance()->stopLogging();
		return -1;
	}

	// Load the Web Bot Auth signing key before any checker threads run.
	if ( Config::instance()->get_bool_value( "bot_auth_enabled" ) ) {
		QFile key_file( Config::instance()->get_string_value( "bot_auth_key_file" ) );
		if ( key_file.open( QIODevice::ReadOnly | QIODevice::Text ) &&
				BotAuth::set_signing_key( key_file.readAll().toStdString(),
							Config::instance()->get_string_value( "bot_auth_key_id" ).toStdString(),
							Config::instance()->get_string_value( "bot_auth_directory_url" ).toStdString() ) ) {
			LOG( "bot auth request signing enabled." );
		} else {
			LOG( "failed to load the bot auth signing key, checks will be sent unsigned." );
		}
	}

	return app.exec();
}
