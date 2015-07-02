
#include "headers/ssl_server.h"
#include "headers/logger.h"

using namespace std;

SSL_Server::SSL_Server( QObject *parent ) : QTcpServer( parent ) {
	LOG( "booting veriflier" );
	m_served_count = 0;

	pool = new QThreadPool(this);
	pool->setMaxThreadCount( Config::instance()->get_int_value( "thread_pool_max" ) );
	LOG( ( QString( "comms max threads: " ) + QString::number( pool->maxThreadCount() ) ) );

	this->setMaxPendingConnections( Config::instance()->get_int_value( "max_pending_conns" ) );
	LOG( ( QString( "max pending conns: " ) + QString::number( this->maxPendingConnections() ) ) );

	m_veriflier_name = Config::instance()->get_string_value( "veriflier_name" );
	m_auth_token = Config::instance()->get_string_value( "auth_token" );
	m_net_timeout = Config::instance()->get_int_value( "net_comms_timeout" );
	m_debug = Config::instance()->get_bool_value( "debug" );
	m_jetmon_server_port = Config::instance()->get_int_value( "jetmon_server_port" );

	int max_checks = Config::instance()->get_int_value( "max_checks" );
	if ( 0 == max_checks || -1 == max_checks ) max_checks = DEFAULT_MAX_CHECKS;
	LOG( ( QString( "max checks: " ) + QString::number( max_checks ) ) );

	m_ssl_config = new QSslConfiguration();
	m_ssl_config->setPeerVerifyMode( QSslSocket::VerifyNone );
	m_ssl_config->setProtocol( QSsl::AnyProtocol );

	QFile keyFile( Config::instance()->get_string_value( "privatekey_file" ) );
	keyFile.open( QFile::ReadOnly );
	QSslKey ssl_key( &keyFile, QSsl::Rsa );
	m_ssl_config->setPrivateKey( ssl_key );
	keyFile.close();

	QFile certFile( Config::instance()->get_string_value( "privatecert_file" ) );
	certFile.open( QFile::ReadOnly );
	QSslCertificate ssl_cert( &certFile );
	m_ssl_config->setLocalCertificate( ssl_cert );
	certFile.close();

	m_checker = new CheckController( m_ssl_config, m_jetmon_server_port, max_checks,
									 m_veriflier_name, m_auth_token, m_net_timeout, m_debug );

	connect( this, SIGNAL( acceptError(QAbstractSocket::SocketError) ), this, SLOT( logError(QAbstractSocket::SocketError) ) );
}

SSL_Server::~SSL_Server() {
	delete m_ssl_config;
	delete m_checker;
	delete pool;
	Logger::instance()->stopLogging();
}

void SSL_Server::incomingConnection( qintptr socketDescriptor ) {
	m_served_count++;
	if ( m_served_count % 50 == 0 )
		LOG( ( QString( "served count: " ) + QString::number( m_served_count ) ).toStdString().c_str() );

	ClientThread *client = new ClientThread( socketDescriptor, m_ssl_config, m_checker, m_veriflier_name,
											m_auth_token, m_net_timeout, m_debug );
	client->setAutoDelete( true );
	pool->start( client );
}

void SSL_Server::logError(QAbstractSocket::SocketError socketError) {
	LOG( QString( socketError ).toStdString().c_str() );
}
