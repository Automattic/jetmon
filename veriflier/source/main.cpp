
#include <QCoreApplication>

#include "headers/config.h"
#include "headers/logger.h"
#include "headers/ssl_server.h"

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

	return app.exec();
}
