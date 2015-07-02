
#include "headers/http_checker.h"
#include "headers/logger.h"

using namespace std;

HTTP_Checker::HTTP_Checker( const int p_net_timeout ) : m_sock( NULL ), m_ssl( NULL ),
						m_host_name( "" ), m_host_dir( "" ), m_port( DEFAULT_HTTP_PORT ),
						m_triptime( 0 ), m_response_code( 0 ), m_net_timeout( p_net_timeout ) {
}

HTTP_Checker::~HTTP_Checker() {
	this->closeConnection();
}

void HTTP_Checker::check( QString p_host_name ) {
	try {
		m_host_name = p_host_name;
		m_host_dir = '/';

		this->parse_host_values();

		if ( this->connect() )
			this->set_host_response( 0 );
	}
	catch ( exception &ex ) {
		LOG( QString( "exception in HTTP_Checker::check(): for host '" ) + m_host_name + QString( "' : " ) + ex.what() );
	}
}

void HTTP_Checker::set_host_response( int redirects ) {
	try {
		struct timeval m_tstart;
		struct timeval m_tend;

		gettimeofday( &m_tstart, &m_tzone );
		QString response = send_http_get();
		if ( response.size() > 0 ) {
			gettimeofday( &m_tend, &m_tzone );
			if ( (m_tend.tv_usec -= m_tstart.tv_usec) < 0 ) {
				m_tend.tv_sec--;
				m_tend.tv_usec += 1000000;
			}
			m_tend.tv_sec -= m_tstart.tv_sec;
			m_triptime = m_tend.tv_sec * 1000000 + ( m_tend.tv_usec );
			if ( response.indexOf( " " ) == 8 ) {
				m_response_code = response.mid( 9, 3 ).toInt();

				// if we have been redirected, get the details and make a recursive call
				if ( ( 300 < m_response_code ) && ( 400 > m_response_code ) ) {
					if ( set_redirect_host_values( response.toStdString().c_str() ) ) {
						this->closeConnection();
						if ( this->connect() ) {
							redirects++;
							if ( Config::instance()->get_int_value( "max_redirects" ) >= redirects )
								this->set_host_response( redirects );
						}
					} else {
						m_response_code = 404;
					}
				}
			} else {
				m_response_code = 404;
			}
		}
	}
	catch( exception &ex ) {
		LOG( QString( "exception in HTTP_Checker::set_host_responses(): for host '" ) + m_host_name + QString( "' : " ) + ex.what() );
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
	}

	if ( -1 != m_host_name.indexOf( "https://" ) ) {
		m_host_name.remove( m_host_name.indexOf( "https://" ), 8 );
		m_port = DEFAULT_HTTPS_PORT;
	}

	if ( -1 != m_host_name.indexOf( ':' ) ) {
		m_port = ( m_host_name.mid( m_host_name.indexOf( ':' ) + 1, min( m_host_name.indexOf( '/' ), m_host_name.length() ) ) ).toInt();
		m_host_name.remove( m_host_name.indexOf( ':' ), min( m_host_name.indexOf( '/' ), m_host_name.length() ) - m_host_name.indexOf( ':' ) );
	}

	if ( -1 != m_host_name.indexOf( '/' ) ) {
		m_host_dir = m_host_name.mid( m_host_name.indexOf( '/' ), m_host_name.length() - m_host_name.indexOf( '/' ) );
		m_host_name.remove( m_host_name.indexOf( '/' ), m_host_name.length() - m_host_name.indexOf( '/' ) );
	}
}

QString HTTP_Checker::send_http_get() {
	QString m_buf = "HEAD " + m_host_dir + " HTTP/1.1\r\n";
			m_buf += "Host: " + m_host_name + "\r\n";
			m_buf += "User-Agent: jetmon/1.0 (Jetpack Site Uptime Monitor by WordPress.com)\r\n";
			m_buf += "Connection: Close\r\n\r\n";

	if ( send_bytes( m_buf ) ) {
		m_buf = get_response();
	} else {
		m_buf = "";
		LOG( "failed to send_bytes()" );
	}
	return m_buf;
}

QString HTTP_Checker::get_response() {
	QString ret_val = "";
	if ( DEFAULT_HTTPS_PORT == m_port ) {
		bool data = m_ssl->waitForReadyRead( m_net_timeout );
		if ( data ) {
			QByteArray a_data = m_ssl->readAll();
			ret_val = a_data.data();
		}
	} else {
		bool data = m_sock->waitForReadyRead( m_net_timeout );
		if ( data ) {
			QByteArray a_data = m_sock->readAll();
			ret_val = a_data.data();
		}
	}

	return ret_val;
}

bool HTTP_Checker::connect() {
	if ( DEFAULT_HTTPS_PORT == m_port ) {
		m_ssl = new QSslSocket();
		m_ssl->connectToHostEncrypted( QString( m_host_name.toStdString().c_str() ), m_port );

		if ( ! m_ssl->waitForEncrypted( m_net_timeout ) ) {
			//LOG( "The SSL handshake failed: ", m_host_name.toStdString().c_str() );
			return false;
		}
	} else {
		m_sock = new QTcpSocket();
		m_sock->connectToHost( m_host_name, m_port );

		if ( ! m_sock->waitForConnected( m_net_timeout ) ) {
			//LOG( "Failed to connect to : ", m_host_name.toStdString().c_str() );
			return false;
		}
	}

	return true;
}

bool HTTP_Checker::closeConnection() {
	if ( m_ssl != NULL ) {
		if ( m_ssl->isOpen() )
			m_ssl->close();
		delete m_ssl;
		m_ssl = NULL;
	}
	if ( m_sock != NULL ) {
		if ( m_sock->isOpen() )
			m_sock->close();
		delete m_sock;
		m_sock = NULL;
	}
	return true;
}

bool HTTP_Checker::send_bytes( QString s_data ) {
	qint64 bytes_sent = 0;

	if ( DEFAULT_HTTPS_PORT == m_port ) {
		bytes_sent = m_ssl->write( s_data.toStdString().c_str(), s_data.length() );
		m_ssl->flush();
		m_ssl->waitForBytesWritten( m_net_timeout );
	} else {
		bytes_sent = m_sock->write( s_data.toStdString().c_str(), s_data.length() );
		m_sock->flush();
		m_sock->waitForBytesWritten( m_net_timeout );
	}

	return ( bytes_sent == s_data.length() );
}

