
#include <QSslConfiguration>
#include "headers/http_checker.h"
#include "headers/logger.h"

using namespace std;

HTTP_Checker::HTTP_Checker( const int p_net_timeout ) : QObject( NULL ), m_ssl_config( NULL ),
	m_sock( NULL ), m_timeout( NULL ), m_host_name( "" ), m_host_dir( "" ),
    m_port( DEFAULT_HTTP_PORT ), m_is_ssl( false ), m_finished( false ),
	m_redirects( 0 ), m_response_code( 0 ), m_net_timeout( p_net_timeout )
{
	m_starttime = QDateTime::currentDateTime();
	m_ssl_config = new QSslConfiguration();
	m_ssl_config->setProtocol( QSsl::SecureProtocols );
}

HTTP_Checker::~HTTP_Checker() {
	this->closeConnection();
	delete m_ssl_config;
}

void HTTP_Checker::check( HealthCheck* p_hc ) {
	try {
		m_hc = p_hc;
		m_host_name = m_hc->monitor_url;
		m_host_dir = '/';

		m_timeout = new QTimer( this );
		QObject::connect( m_timeout, SIGNAL( timeout() ), this, SLOT( timed_out() ) );
		m_timeout->start( m_net_timeout );

		this->parse_host_values();
		this->connect();
	}
	catch ( exception &ex ) {
		LOG( QString( "exception in HTTP_Checker::check(): for host '" ) + m_host_name + "' : " + ex.what() );
	}
}

void HTTP_Checker::process_response() {
	// if we have been redirected, get the details and make a recursive call
	if ( ( 300 < m_response_code ) && ( 400 > m_response_code ) ) {
		m_redirects++;
		if ( Config::instance()->get_int_value( "max_redirects" ) >= m_redirects &&
			set_redirect_host_values( m_response.toStdString().c_str() ) ) {
			this->closeConnection();
			LOG( QString::number( m_starttime.msecsTo( QDateTime::currentDateTime() ) ) +
				 "  \t: STAGE 1 :\tredirecting to " + m_host_name + m_host_dir );
			m_response_code = 0;
			this->connect();
		} else {
			// Note we leave the 3xx response code so this site is marked as up
			finish_request();
		}
	} else {
		finish_request();
	}
}

bool HTTP_Checker::set_redirect_host_values( QString p_content ) {
	try {
		QString p_lcase_search = p_content.toLower();
		if ( -1 == p_lcase_search.indexOf( "location: " ) )
			return false;

		p_content = p_content.mid( p_lcase_search.indexOf( "location: " ) + 10, p_content.length() - ( p_lcase_search.indexOf( "location: " ) + 10 ) );
		if ( -1 == p_content.indexOf( "\r\n" ) )
			return false;

		p_content.remove( p_content.indexOf( "\r\n" ), p_content.length() - p_content.indexOf( "\r\n" ) );

		// keep a copy for relative location redirects
		QString hostname_backup = m_host_name;
		m_host_name = p_content;
		m_port = DEFAULT_HTTP_PORT;
		m_host_dir = '/';
		m_is_ssl = false;

		this->parse_host_values();

		// this is a relative location redirect, reinstate hostname
		if ( 0 == m_host_name.size() )
			m_host_name = hostname_backup;

		return true;
	}
	catch( exception &ex ) {
		LOG( QString( "exception in HTTP_Checker::set_redirect_host_values(): " ) + ex.what() );
		return false;
	}
}

void HTTP_Checker::parse_host_values() {
	if ( -1 != m_host_name.indexOf( "http://" ) ) {
		m_host_name.remove( m_host_name.indexOf( "http://" ), 7 );
		m_is_ssl = false;
	}

	if ( -1 != m_host_name.indexOf( "https://" ) ) {
		m_host_name.remove( m_host_name.indexOf( "https://" ), 8 );
		m_is_ssl = true;
		m_port = DEFAULT_HTTPS_PORT;
	}

	size_t s_pos = m_host_name.indexOf( '/' );
	size_t q_pos = m_host_name.indexOf( '?' );
	size_t c_pos = m_host_name.indexOf( ':' );
	size_t f_pos = m_host_name.indexOf( '#' );

	if ( ( c_pos < s_pos ) && ( c_pos < q_pos ) && ( c_pos < f_pos ) ) {
		int new_port = m_host_name.mid( c_pos + 1, min( s_pos, (size_t)m_host_name.length() ) ).toInt();
		if ( 0 < new_port ) {
			m_port = new_port;
			m_host_name.remove( c_pos, min( s_pos, (size_t)m_host_name.length() ) - c_pos );
			// recalc since we've erased some characters
			s_pos = m_host_name.indexOf( '/' );
			q_pos = m_host_name.indexOf( '?' );
			f_pos = m_host_name.indexOf( '#' );
		}
	}

	if ( string::npos != s_pos || string::npos != q_pos || string::npos != f_pos ) {
		int m_pos = min( min( s_pos, q_pos ), f_pos );
		m_host_dir = m_host_name.mid( m_pos, m_host_name.length() - m_pos );
		if ( 0 == m_host_dir.length() || '?' == m_host_dir[0] || '#' == m_host_dir[0] ) {
			m_host_dir = "/" + m_host_dir;
		}
		m_host_name.remove( m_pos, m_host_name.length() - m_pos );
	}
}

void HTTP_Checker::parse_response_code( QByteArray a_data ) {
	if ( 0 == a_data.size() ) {
		return;
	}

	m_response = a_data.toStdString().c_str();
	if ( m_response.indexOf( " " ) == 8 ) {
		m_response_code = m_response.mid( 9, 3 ).toInt();
	} else {
		m_response_code = -1;
	}
}

bool HTTP_Checker::send_http_get() {
	QString m_buf = "HEAD " + m_host_dir + " HTTP/1.1\r\n";
			m_buf += "Host: " + m_host_name + "\r\n";
			m_buf += "User-Agent: jetmon/1.0 (Jetpack Site Uptime Monitor by WordPress.com)\r\n";
			m_buf += "Connection: close\r\n\r\n";

	qint64 bytes_sent = m_sock->write( m_buf.toStdString().c_str(), m_buf.length() );

	return ( bytes_sent == m_buf.length() );
}

void HTTP_Checker::connect() {
	if ( m_is_ssl ) {
		m_sock = new QSslSocket();
		((QSslSocket*)m_sock)->setSslConfiguration( *m_ssl_config );
		QObject::connect( ((QSslSocket*)m_sock), SIGNAL( connected() ), this, SLOT( connected() ) );
		QObject::connect( ((QSslSocket*)m_sock), SIGNAL( readyRead() ), this, SLOT( readyRead() ) );
		QObject::connect( ((QSslSocket*)m_sock), SIGNAL( error( QAbstractSocket::SocketError) ), this, SLOT( connectionError( QAbstractSocket::SocketError) ) );
		((QSslSocket*)m_sock)->connectToHostEncrypted( m_host_name, m_port );
	} else {
		m_sock = new QTcpSocket();
		QObject::connect( m_sock, SIGNAL( connected() ), this, SLOT( connected() ) );
		QObject::connect( m_sock, SIGNAL( readyRead() ), this, SLOT( readyRead() ) );
		QObject::connect( m_sock, SIGNAL( error( QAbstractSocket::SocketError) ), this, SLOT( connectionError( QAbstractSocket::SocketError) ) );
		m_sock->connectToHost( m_host_name, m_port );
	}
}

void HTTP_Checker::connected() {
	try {
		if ( ! m_sock->isOpen() ) {
			finish_request();
			return;
		}
		send_http_get();
	}
	catch( exception &ex ) {
		LOG( QString( "exception in HTTP_Checker::connected(): for host '" ) + m_host_name + "' : " + ex.what() );
	}
}

void HTTP_Checker::connectionError( QAbstractSocket::SocketError err ) {
	//LOG( "Connection Error[" + QString::number( err ) + "]: " + m_sock->errorString() );
	finish_request();
}

void HTTP_Checker::readyRead() {
	QByteArray a_data = m_sock->readAll();

	if ( 0 == a_data.length() ) {
		LOG( "NO data received from the check." );
		return;
	}

	if ( 0 == m_response_code ) {
		parse_response_code( a_data );
		if ( 0 != m_response_code ) {
			process_response();
		}
	}
}

void HTTP_Checker::closeConnection() {
	if ( m_sock != NULL ) {
		if ( m_sock->isOpen() )
			m_sock->close();
		m_sock->deleteLater();
	}
}

void HTTP_Checker::timed_out() {
	if ( m_sock != NULL ) {
		if ( m_sock->isOpen() )
			m_sock->disconnectFromHost();
	}
	m_timeout->stop();
	finish_request();
}

void HTTP_Checker::finish_request() {
	if ( ! m_finished ) {
		m_finished = true;
		emit finished( this, m_hc );
	}
}

