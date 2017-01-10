
#include "../headers/jetmon_server.h"

JetmonServer::JetmonServer( QObject *parent, const QSslConfiguration *ssl_config, QString jetmon_server, int jetmon_server_port ) :
	QObject(parent), m_jetmon_server( jetmon_server ), m_jetmon_server_port( jetmon_server_port )
{
	m_socket = new QSslSocket();
	m_socket->setSslConfiguration( *ssl_config );

	QObject::connect( m_socket, SIGNAL( connected() ), this, SLOT( connected() ) );
	QObject::connect( m_socket, SIGNAL( readyRead() ), this, SLOT( readyRead() ) );
	QObject::connect( m_socket, SIGNAL( error( QAbstractSocket::SocketError) ), this, SLOT( connectionError( QAbstractSocket::SocketError) ) );
}

void JetmonServer::sendData( QByteArray status_data ) {
	m_timer = QDateTime::currentDateTime();
	m_status_data = status_data;
	m_socket->connectToHostEncrypted( m_jetmon_server, m_jetmon_server_port );
}

void JetmonServer::connected() {
	if ( m_socket->isEncrypted() || ( ! m_socket->isOpen() ) ) {
		emit finished( this, 0, m_timer.msecsTo( QDateTime::currentDateTime() ) );
		return;
	}

	LOG( QString::number( m_timer.msecsTo( QDateTime::currentDateTime() ) ) +
		QString( "\t\t: SENDING :\tconnected to :" ) + m_jetmon_server );
	m_timer = QDateTime::currentDateTime();

	m_socket->write( m_status_data );
	m_socket->flush();
}

void JetmonServer::connectionError( QAbstractSocket::SocketError err ) {
	LOG( "Connection Error[" + QString::number( err ) + "]: " + m_jetmon_server + " : "+ m_socket->errorString() );
	emit finished( this, 0, m_timer.msecsTo( QDateTime::currentDateTime() ) );
}

void JetmonServer::readyRead() {
	QByteArray a_data = m_socket->readAll();
	this->closeConnection();

	if ( 0 == a_data.length() ) {
		LOG( "NO data returned when reading jetmon response." );
		emit finished( this, 0, m_timer.msecsTo( QDateTime::currentDateTime() ) );
		return;
	}

	QJsonDocument json_doc = parse_json_response( a_data );

	if ( json_doc.isEmpty() || json_doc.isNull() ) {
		LOG( "Invalid JSON document format." );
		emit finished( this, 0, m_timer.msecsTo( QDateTime::currentDateTime() ) );
		return;
	}

	QJsonValue response = json_doc.object().value( "response" );
	if ( response.isNull() ) {
		LOG( "Missing 'response' JSON value." );
		emit finished( this, 0, m_timer.msecsTo( QDateTime::currentDateTime() ) );
		return;
	}

	emit finished( this, 1, m_timer.msecsTo( QDateTime::currentDateTime() ) );
}

void JetmonServer::closeConnection() {
	if ( m_socket->isOpen() )
		m_socket->close();
	m_socket->deleteLater();
}

QJsonDocument JetmonServer::parse_json_response( QByteArray &raw_data ) {
	QJsonDocument ret_val;
	QString s_data = raw_data.data();

	if ( ( -1 == s_data.indexOf( "{" ) ) || ( -1 == s_data.lastIndexOf( "}" ) ) ) {
		LOG( "Invalid JSON response format.\n\n" + s_data );
		return ret_val;
	}

	s_data = s_data.mid( s_data.indexOf( "{" ), s_data.lastIndexOf( "}" ) - s_data.indexOf( "{" ) + 1 );
	ret_val = QJsonDocument::fromJson( s_data.toUtf8() );
	return ret_val;
}

