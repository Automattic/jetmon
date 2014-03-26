
#include "headers/client_thread.h"
#include "headers/logger.h"

#include <QtNetwork>
#include <QtNetwork/QSslSocket>
#include <QJsonObject>

#include <iostream>

ClientThread::ClientThread( qintptr p_sock, const QString &p_veriflier_name, const QString &p_auth_token,
							const int p_net_comms_timeout, const bool p_debug )
	: m_sock( p_sock ), m_veriflier_name( p_veriflier_name ), m_auth_token( p_auth_token ),
	  m_net_comms_timeout( p_net_comms_timeout ), m_running( true ), m_debug( p_debug ), m_site_status_request( false ) {

	timer = QDateTime::currentDateTime();
}

ClientThread::~ClientThread() {
	delete m_socket;
}

void ClientThread::run() {

	m_socket = new QSslSocket();

	if ( ! m_socket->setSocketDescriptor( m_sock ) ) {
		delete m_socket;
		LOG ( "Unable to set file descriptor for server SSL connection." );
		return;
	}

	m_socket->setPeerVerifyMode( QSslSocket::VerifyNone );
	m_socket->setProtocol( QSsl::AnyProtocol );
	m_socket->setPrivateKey( Config::instance()->get_string_value( "privatekey_file" ) );
	m_socket->setLocalCertificate( Config::instance()->get_string_value( "privatecert_file" ) );
	m_socket->startServerEncryption();

	if ( ! m_socket->waitForEncrypted() ) {
		LOG( "Unable to negotiate SSL for server request." );
		m_socket->close();
		return;
	}

	if ( m_socket->encryptedBytesToWrite() )
		m_socket->flush();

	// Store the jetmon server's address for our reply
	m_jetmon_server = m_socket->peerAddress().toString();

	if ( m_socket->waitForReadyRead( m_net_comms_timeout ) )
		this->readRequest();

	if ( ! m_site_status_request ) {
		m_socket->close();
		return;
	}

	// Tell the Jetmon server we have received and validated the request
	this->sendOK();
	m_socket->close();

	if ( m_debug ) {
		LOG( QString::number( timer.msecsTo( QDateTime::currentDateTime() ) ) +
			 QString( "\t: STAGE 1 :\t" ) + QString( m_monitor_url.toString() ) );
	}

	// Time the host check
	timer = QDateTime::currentDateTime();

	// Start the check on our side
	this->performHostCheck();
}

ClientThread::QueryType ClientThread::get_request_type( QByteArray &raw_data ) {
	QString s_data = raw_data.data();
	int pos = s_data.indexOf( "HTTP/1." );

	if ( -1 == pos ) {
		LOG( "Invalid HTTP request format." );
		this->sendError( "Invalid HTTP request format." );
		m_running = false;
		return ClientThread::UnknownQuery;
	}

	s_data = s_data.left( pos - 1 );

	if ( s_data.startsWith( "GET /get/status") )
		return ClientThread::ServiceRunning;

	if ( s_data.startsWith( "GET /get/host-status") )
		return ClientThread::SiteStatusCheck;;

	return ClientThread::UnknownQuery;
}

QJsonDocument ClientThread::parse_json_request( QByteArray &raw_data ) {
	QJsonDocument ret_val;
	QString s_data = raw_data.data();
	int pos = s_data.indexOf( "HTTP/1." );

	if ( -1 == pos ) {
		LOG( "Invalid HTTP request format." );
		this->sendError( "Invalid HTTP request format." );
		m_running = false;
		return ret_val;
	}

	s_data = s_data.left( pos - 1 );
	s_data = s_data.right( s_data.length() - s_data.indexOf( "/" ) );
	s_data = s_data.right( s_data.length() - s_data.indexOf( "?d=" ) - 3 );

	ret_val = QJsonDocument::fromJson( s_data.toUtf8() );

	return ret_val;
}

QJsonDocument ClientThread::parse_json_response( QByteArray &raw_data ) {
	QJsonDocument ret_val;
	QString s_data = raw_data.data();

	if ( ( -1 == s_data.indexOf( "{" ) ) || ( -1 == s_data.lastIndexOf( "}" ) ) ) {
		LOG( "Invalid JSON response format." );
		m_running = false;
		return ret_val;
	}

	s_data = s_data.mid( s_data.indexOf( "{" ), s_data.lastIndexOf( "}" ) );
	ret_val = QJsonDocument::fromJson( s_data.toUtf8() );
	return ret_val;
}

void ClientThread::readResponse() {
	QByteArray a_data = m_socket->readAll();

	if ( 0 == a_data.length() ) {
		m_running = false;
		return;
	}

	QJsonDocument json_doc = parse_json_response( a_data );

	if ( json_doc.isEmpty() || json_doc.isNull() ) {
		LOG( "Invalid JSON document format." );
		m_running = false;
		return;
	}

	QJsonValue response = json_doc.object().value( "response" );
	if ( response.isNull() ) {
		LOG( "Missing 'response' JSON value." );
		m_running = false;
		return;
	}

	if ( 1 != response.toInt() )
		LOG( QString( "Jetmon server FAILED to received the response: " ) + json_doc.toJson() );

	m_running = false;
}

void ClientThread::readRequest() {
	QByteArray a_data = m_socket->readAll();

	if ( 0 == a_data.length() )
		return;

	QueryType type = get_request_type( a_data );

	if ( type == ClientThread::UnknownQuery )
		return;

	if ( type == ClientThread::ServiceRunning ) {
		this->sendServiceOK();
		LOG( "replied to service status check" );
		return;
	}

	QJsonDocument json_doc = parse_json_request( a_data );
	if ( json_doc.isEmpty() || json_doc.isNull() ) {
		LOG( "Invalid JSON document format." );
		this->sendError( "Invalid JSON document format." );
		m_running = false;
		return;
	}

	QJsonValue client_auth_token = json_doc.object().value( "auth_token" );
	if ( client_auth_token.isNull() ) {
		LOG( "Missing 'auth_token' JSON value." );
		this->sendError( "Missing 'auth_token' JSON value." );
		m_running = false;
		return;
	}

	// TODO: validate token against whitelist table of IP and auth_token

	m_blog_id = json_doc.object().value( "blog_id" );
	if ( m_blog_id.isNull() ) {
		LOG( "Missing 'blog_id' JSON value." );
		this->sendError( "Missing 'blog_id' JSON value." );
		m_running = false;
		return;
	}

	m_monitor_url = json_doc.object().value( "monitor_url" );
	if ( m_monitor_url.isNull() ) {
		LOG( "Missing 'monitor_url' JSON value." );
		this->sendError( "Missing 'monitor_url' JSON value." );
		m_running = false;
		return;
	}

	// if we made it here we are a-for-away
	m_site_status_request = true;
}

void ClientThread::performHostCheck() {
	HTTP_Checker *http_check = new HTTP_Checker( m_net_comms_timeout );
	http_check->check( m_monitor_url.toString() );

	if ( http_check->get_rtt() > 0 && 400 > http_check->get_response_code() )
		sendResult( HOST_ONLINE );
	else
		sendResult( HOST_DOWN );

	delete http_check;
	m_running = false;
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

QString ClientThread::get_http_content( int status, const QString &error ) {
	QString ret_val = "{\"veriflier\":\"";
	ret_val += m_veriflier_name;
	ret_val += "\",\"auth_token\":\"";
	ret_val += m_auth_token;
	ret_val += "\",\"m_url\":\"";
	ret_val += m_monitor_url.toString();
	ret_val += "\",\"blog_id\":";
	ret_val += QString::number( m_blog_id.toInt() );
	ret_val += ",\"status\":";
	ret_val += QString::number( status );
	if ( error.length() > 0 ) {
		ret_val += ",\"error\":\"";
		ret_val += error;
		ret_val += "\"";
	}
	ret_val += "}\n";

	return ret_val;
}

void ClientThread::sendServiceOK() {
	m_socket->write( "OK" );
	m_socket->flush();
	m_socket->waitForBytesWritten( m_net_comms_timeout );
}

void ClientThread::sendOK() {
	QString s_data = get_http_content( 1 );
	QString s_response = get_http_reply_header( "200 OK", s_data );

	m_socket->write( s_response.toStdString().c_str() );
	m_socket->flush();
	m_socket->waitForBytesWritten( m_net_comms_timeout );
}

void ClientThread::sendError( const QString errorString ) {
	QString s_data = get_http_content( -1, errorString );
	QString s_response = get_http_reply_header( "404 Not Found", s_data );

	m_socket->write( s_response.toStdString().c_str() );
	m_socket->flush();
	m_socket->waitForBytesWritten( m_net_comms_timeout );
}

QString ClientThread::get_http_request_content( int status ) {
	QString ret_val = "{\"veriflier\":\"";
	ret_val += m_veriflier_name;
	ret_val += "\",\"auth_token\":\"";
	ret_val += m_auth_token;
	ret_val += "\",\"m_url\":\"";
	ret_val += m_monitor_url.toString();
	ret_val += "\",\"blog_id\":";
	ret_val += QString::number( m_blog_id.toInt() );
	ret_val += ",\"status\":";
	ret_val += QString::number( status );
	ret_val += "}";

	return ret_val;
}

QString ClientThread::get_http_request_header( int status ) {
	QString ret_val = "GET /put/host-status?d=";
	ret_val += QUrl::toPercentEncoding( get_http_request_content( status ) ).data();
	ret_val += " HTTP/1.1\r\nHost: ";
	ret_val += m_jetmon_server;
	ret_val += "\r\nConnection: close\r\n\r\n";

	return ret_val;
}

void ClientThread::sendResult( int status ) {
	if ( m_debug ) {
		LOG( QString::number( timer.msecsTo( QDateTime::currentDateTime() ) ) +
			QString( "\t: STAGE 2 :\t" ) + QString( m_monitor_url.toString() ) +
			QString( " ->connecting back to :" ) + m_jetmon_server );
		timer = QDateTime::currentDateTime();
	}

	QString s_response = get_http_request_header( status );

	m_socket->connectToHostEncrypted( m_jetmon_server, Config::instance()->get_int_value( "jetmon_server_port" ) );
	m_socket->waitForEncrypted( m_net_comms_timeout );

	if ( m_socket->isEncrypted() ) {

		m_socket->write( s_response.toStdString().c_str() );
		m_socket->flush();
		m_socket->waitForBytesWritten( m_net_comms_timeout );

		if ( m_socket->waitForReadyRead( m_net_comms_timeout ) )
			this->readResponse();
	} else {
		if ( m_debug ) {
			LOG( QString::number( timer.msecsTo( QDateTime::currentDateTime() ) ) +
				QString( "\t: STAGE 2 :\t" ) + QString( m_monitor_url.toString() ) +
				QString( " -< failed to connect to to :" ) + m_jetmon_server );
			timer = QDateTime::currentDateTime();
		}
	}

	if ( m_socket->isOpen() )
		m_socket->close();

	if ( m_debug ) {
		LOG( QString::number( timer.msecsTo( QDateTime::currentDateTime() ) ) +
			QString( "\t: STAGE 3 :\t" ) + QString( m_monitor_url.toString() ) + QString( " - " ) +
			QString::number( status ) );
	}
}

