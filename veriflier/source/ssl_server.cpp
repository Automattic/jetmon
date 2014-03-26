
#include "headers/ssl_server.h"
#include "headers/logger.h"

#include <iostream>

using namespace std;

SSL_Server::SSL_Server( QObject *parent ) : QTcpServer( parent ) {
	LOG( "booting veriflier" );
	m_served_count = 0;
	QThreadPool::globalInstance()->setMaxThreadCount( Config::instance()->get_int_value( "thread_pool_max" ) );
	m_veriflier_name = Config::instance()->get_string_value( "veriflier_name" );
	m_auth_token = Config::instance()->get_string_value( "auth_token" );
	m_net_comms_timeout = Config::instance()->get_int_value( "net_comms_timeout" );
	m_debug = Config::instance()->get_bool_value( "debug" );
}

SSL_Server::~SSL_Server() {
	Logger::instance()->stopLogging();
}

void SSL_Server::incomingConnection( qintptr socketDescriptor ) {
	m_served_count++;
	if ( m_served_count % 50 == 0 )
		LOG( ( QString( "served count: " ) + QString::number( m_served_count ) ).toStdString().c_str() );

	ClientThread *client = new ClientThread( socketDescriptor, m_veriflier_name,
											m_auth_token, m_net_comms_timeout, m_debug );
	QThreadPool::globalInstance()->start( client );
}

