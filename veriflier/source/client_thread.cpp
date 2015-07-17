
#include "headers/client_thread.h"
#include "headers/logger.h"

#include <QtNetwork>
#include <QtNetwork/QSslSocket>
#include <QtNetwork/QTcpSocket>
#include <QJsonObject>

ClientThread::ClientThread( qintptr sock, const QSslConfiguration *ssl_config,
							CheckController *checker, const QString &veriflier_name,
							const QString &auth_token, const int net_timeout, const bool debug )
	: m_sock( sock ), m_socket( NULL ), m_ssl_config( ssl_config ), m_checker( checker ),
	m_veriflier_name( veriflier_name ), m_auth_token( auth_token ), m_net_timeout( net_timeout ),
	m_debug( debug ), m_site_status_request( false )
{
	;
}

ClientThread::~ClientThread() {
	delete m_socket;
}

void ClientThread::run() {
	m_socket = new QSslSocket();

	if ( ! m_socket->setSocketDescriptor( m_sock ) ) {
		LOG ( "Unable to set file descriptor for server SSL connection." );
		return;
	}

	m_socket->setSslConfiguration( *m_ssl_config );
	m_socket->startServerEncryption();

	if ( ! m_socket->waitForEncrypted() ) {
		LOG( "Unable to negotiate SSL for server request: " + m_socket->errorString() );
		m_socket->close();
		return;
	}

	if ( m_socket->encryptedBytesToWrite() ) {
		m_socket->flush();
	}

	// Store the jetmon server's address for our reply
	m_jetmon_server = m_socket->peerAddress().toString();

	if ( m_socket->waitForReadyRead( m_net_timeout ) ) {
		this->readRequest();
	}

	if ( ! m_site_status_request ) {
		m_socket->close();
		return;
	}

	// Tell the Jetmon server we have received and validated the request
	this->sendOK();
	m_socket->close();

	if ( m_debug ) {
		LOG( "RECV\t: ------- :\t " + QString::number( m_checks.size() ) );
	}
	m_checker->addChecks( m_checks );
}

ClientThread::QueryType ClientThread::get_request_type( QByteArray &raw_data ) {
	QString s_data = raw_data.data();
	int pos = s_data.indexOf( "HTTP/1." );

	if ( -1 == pos ) {
		LOG( "Invalid HTTP request format." );
		this->sendError( "Invalid HTTP request format." );
		return ClientThread::UnknownQuery;
	}

	s_data = s_data.left( pos - 1 );

	if ( s_data.startsWith( "GET /get/status") ) {
		return ClientThread::ServiceRunning;
	}

	if ( s_data.startsWith( "GET /get/host-status") ) {
		return ClientThread::SiteStatusCheck;
	}

	if ( s_data.startsWith( "POST /get/host-status") ) {
		return ClientThread::SiteStatusPostCheck;
	}

	return ClientThread::UnknownQuery;
}

QJsonDocument ClientThread::parse_json_request( QByteArray &raw_data ) {
	QJsonDocument ret_val;
	QString s_data = raw_data.data();
	int pos = s_data.indexOf( "HTTP/1." );

	if ( -1 == pos ) {
		LOG( "Invalid HTTP request format." );
		this->sendError( "Invalid HTTP request format." );
		return ret_val;
	}

	s_data = s_data.left( pos - 1 );
	s_data = s_data.right( s_data.length() - s_data.indexOf( "/" ) );
	s_data = s_data.right( s_data.length() - s_data.indexOf( "?d=" ) - 3 );

	ret_val = QJsonDocument::fromJson( s_data.toUtf8() );

	return ret_val;
}

QJsonDocument ClientThread::parse_json_request_post( QByteArray &raw_data ) {
	QJsonDocument ret_val;
	QString s_data = raw_data.data();
	int pos = s_data.indexOf( " HTTP/" );

	if ( -1 == pos ) {
		LOG( "Invalid HTTP request format." );
		this->sendError( "Invalid HTTP request format." );
		return ret_val;
	}

	pos = s_data.indexOf( "\r\n\r\n" );

	if ( -1 == pos ) {
		LOG( "Invalid HTTP request format." );
		this->sendError( "Invalid HTTP request format." );
		return ret_val;
	}

	s_data = s_data.right( s_data.length() - pos - 4 );
	ret_val = QJsonDocument::fromJson( s_data.toUtf8() );

	return ret_val;
}

void ClientThread::readRequest() {
	QByteArray a_data = m_socket->readAll();

	if ( 0 == a_data.length() ) {
		LOG( "NO data received from the jetmon server." );
		return;
	}

	QueryType type = get_request_type( a_data );

	if ( type == ClientThread::UnknownQuery ) {
		this->sendError( "Unknown query received: " + QString::number( type ) );
		LOG( "unknown query received: " + QString::number( type ) );
		return;
	}
	if ( type == ClientThread::ServiceRunning ) {
		this->sendServiceOK();
		LOG( "replied to service status check" );
		return;
	}

	QJsonDocument json_doc = ( type == ClientThread::SiteStatusPostCheck ? parse_json_request_post( a_data ) : parse_json_request( a_data ) );
	if ( json_doc.isEmpty() || json_doc.isNull() ) {
		LOG( "Invalid JSON document format." );
		this->sendError( "Invalid JSON document format." );
		return;
	}

	QJsonValue client_auth_token = json_doc.object().value( "auth_token" );
	if ( client_auth_token.isNull() ) {
		LOG( "Missing 'auth_token' JSON value." );
		this->sendError( "Missing 'auth_token' JSON value." );
		return;
	}

	m_site_status_request = parse_requests( type, json_doc );
}

bool ClientThread::parse_requests( QueryType type, QJsonDocument json_doc ) {
	QJsonValue blog_id, monitor_url;

	if ( type == ClientThread::SiteStatusCheck ) {
		blog_id = json_doc.object().value( "blog_id" );
		if ( blog_id.isNull() ) {
			LOG( "Missing 'blog_id' JSON value." );
			this->sendError( "Missing 'blog_id' JSON value." );
			return false;
		}

		monitor_url = json_doc.object().value( "monitor_url" );
		if ( monitor_url.isNull() ) {
			LOG( "Missing 'monitor_url' JSON value." );
			this->sendError( "Missing 'monitor_url' JSON value." );
			return false;
		}

		HealthCheck *hc = new HealthCheck();
		hc->ct = NULL;
		hc->received = QDateTime::currentDateTime();
		hc->jetmon_server = m_jetmon_server;
		hc->monitor_url = monitor_url.toString();
		hc->blog_id = blog_id.toInt();

		this->m_checks.append( hc );
	} else {
		QJsonArray jArr = json_doc.object()["checks"].toArray();
		if ( jArr.isEmpty() ) {
			this->sendError( "Missing 'checks' JSON array." );
			LOG( "Missing 'checks' JSON array." );
			return false;
		}

		for ( int loop = 0; loop < jArr.count(); loop++ ) {
			blog_id = jArr.at( loop ).toObject().value( "blog_id" );
			if ( blog_id.isNull() ) {
				LOG( "Missing 'blog_id' JSON value for array index " + QString::number( loop ) );
				continue;
			}

			monitor_url = jArr.at( loop ).toObject().value( "monitor_url" );
			if ( monitor_url.isNull() ) {
				LOG( "Missing 'monitor_url' JSON value for array index " + QString::number( loop ) );
				continue;
			}

			HealthCheck *hc = new HealthCheck();
			hc->ct = NULL;
			hc->received = QDateTime::currentDateTime();
			hc->jetmon_server = m_jetmon_server;
			hc->monitor_url = monitor_url.toString();
			hc->blog_id = blog_id.toInt();

			this->m_checks.append( hc );
		}
	}

	return true;
}

void ClientThread::sendServiceOK() {
	m_socket->write( "OK" );
	m_socket->flush();
	m_socket->waitForBytesWritten( m_net_timeout );
}

void ClientThread::sendOK() {
	QString s_data = get_http_content( 1 );
	QString s_response = get_http_reply_header( "200 OK", s_data );

	m_socket->write( s_response.toStdString().c_str() );
	m_socket->flush();
	m_socket->waitForBytesWritten( m_net_timeout );
}

void ClientThread::sendError( const QString errorString ) {
	QString s_data = get_http_content( -1, errorString );
	QString s_response = get_http_reply_header( "404 Not Found", s_data );

	m_socket->write( s_response.toStdString().c_str() );
	m_socket->flush();
	m_socket->waitForBytesWritten( m_net_timeout );
}

QString ClientThread::get_http_content( int status, const QString &error ) {
	QString ret_val = "{\"veriflier\":\"";
	ret_val += m_veriflier_name;
	ret_val += "\",\"auth_token\":\"";
	ret_val += m_auth_token;
	ret_val += "\",\"status\":";
	ret_val += QString::number( status );
	if ( error.length() > 0 ) {
		ret_val += ",\"error\":\"";
		ret_val += error;
		ret_val += "\"";
	}
	ret_val += "}\n";

	return ret_val;
}

QString ClientThread::get_http_reply_header( const QString &http_code, const QString &p_data) {
	QString ret_val = "HTTP/1.1 ";
	ret_val += http_code;
	ret_val += "\r\nContent-Type: application/json\r\n";
	ret_val += "Content-Length: ";
	ret_val += QString::number( p_data.length() );
	ret_val += "\r\nConnection: close\r\n\r\n";
	ret_val += p_data;

	return ret_val;
}
