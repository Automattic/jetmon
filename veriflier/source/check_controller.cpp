
#include "headers/check_controller.h"
#include "headers/logger.h"

#include <QMutexLocker>
#include <QJsonDocument>
#include <QJsonArray>

CheckController::CheckController( const QSslConfiguration *ssl_config, const int jetmon_server_port,
								  const int max_checks, const QString &veriflier_name,
								  const QString &auth_token, const int net_timeout, const bool debug )
	: m_ssl_config( ssl_config ), m_jetmon_server_port( jetmon_server_port ), m_socket( NULL ),
	  m_max_checks( max_checks ), m_checking( 0 ), m_checked( 0 ), m_veriflier_name( veriflier_name ),
	  m_auth_token( auth_token ), m_net_timeout( net_timeout ), m_debug( debug )
{
	m_checks.resize( 0 );
	m_ticker = new QTimer( this );
	connect( m_ticker, SIGNAL( timeout() ), this, SLOT( ticked() ) );
	m_ticker->start( 5000 );
}

CheckController::~CheckController() {
	m_ticker->stop();
	delete m_ticker;
	delete m_socket;
}

void CheckController::finishedChecking( qint64 blog_id, int status ) {
	QJsonDocument json_doc;
	QJsonObject json_obj, arr_result;
	QJsonArray checkArray;
	m_checked++;
	m_check_lock.lock();
	for ( int loop = 0; loop < m_checks.size(); loop++ ) {
		if ( m_checks[loop]->blog_id == blog_id ) {
			if ( NULL == m_checks[loop]->ct ) {
				LOG( "deleting a blog_id that does not have a check thread assigned?: " + QString::number( blog_id ) );
			}

			arr_result.insert( "blog_id", QJsonValue( blog_id ) );
			arr_result.insert( "status", QJsonValue( status ) );
			QMap<QString, QJsonDocument>::iterator itr = m_check_results.find( m_checks[loop]->jetmon_server );
			if ( m_check_results.end() != itr ) {
				json_doc = itr.value();
				checkArray = json_doc.object()["checks"].toArray();
			}
			checkArray.append( arr_result );
			json_obj.insert( "auth_token", QJsonValue( m_auth_token ) );
			json_obj.insert( "checks", checkArray );
			json_doc.setObject( json_obj );
			m_check_results.insert( m_checks[loop]->jetmon_server, json_doc );

			m_checks[loop]->ct->quit();
			m_checks[loop]->ct->deleteLater();
			HealthCheck *ptr = m_checks[loop];
			m_checks.remove( loop );
			delete ptr;
			m_checking--;
			break;
		}
	}
	if ( m_checking < m_max_checks ) {
		for ( int loop = 0; loop < m_checks.size(); loop++ ) {
			if ( NULL == m_checks[loop]->ct ) {
				startChecking( m_checks[loop] );
				m_check_lock.unlock();
				return;
			}
		}
	}
	m_check_lock.unlock();
}

inline bool CheckController::haveCheck( qint64 blog_id ) {
	for ( int loop = 0; loop < m_checks.size(); loop++ ) {
		if ( m_checks[loop]->blog_id == blog_id ) {
			return true;
		}
	}
	return false;
}

void CheckController::startChecking( HealthCheck* hc ) {
	m_checking++;
	CheckThread *ct = new CheckThread( m_ssl_config, m_net_timeout, m_debug );
	connect( ct, SIGNAL( resultReady(qint64, int) ), this, SLOT( finishedChecking(qint64, int) ) );
	hc->ct = ct;
	ct->setMonitorUrl( hc->monitor_url );
	ct->setBlogID( hc->blog_id );
	ct->setTimer( hc->received );
	ct->start();
}

void CheckController::addCheck( HealthCheck* hc ) {
	if ( haveCheck( hc->blog_id ) ) {
		LOG( "Already have this blog in the check list: " + QString::number( hc->blog_id ) );
		return;
	}

	m_check_lock.lock();
	m_checks.append( hc );
	if ( m_checking < m_max_checks ) {
		startChecking( hc );
	}
	m_check_lock.unlock();
}

void CheckController::addChecks( QVector<HealthCheck *> hcs ) {
	for ( int loop = 0; loop < hcs.size(); loop++ ) {
		this->addCheck( hcs[loop] );
	}
}

void CheckController::ticked() {
	this->sendResults();

	if ( m_checks.size() > 0 || m_checked > 0 ) {
		LOG( "total - " + QString::number( m_checks.size() ) +
			" : checking - " + QString::number( m_checking ) +
			" : checked = " + QString::number( m_checked ) );
	}
	m_checked = 0;
}

void CheckController::sendResults() {
	if ( 0 == m_check_results.size() )
		return;

	m_check_lock.lock();
	QMap<QString, QJsonDocument> sendMap( m_check_results );
	m_check_results.clear();
	m_check_lock.unlock();

	QMap<QString, QJsonDocument>::const_iterator itr = sendMap.begin();
	while ( itr != sendMap.end() ) {
		QByteArray arr_data;
		arr_data.append( post_http_header( QString( itr.key().toStdString().c_str() ), itr.value().toJson().size() ) );
		arr_data.append( itr.value().toJson() );
		LOG( "\t: SENDING : " + QString::number( itr.value().object()["checks"].toArray().size() ) + " results" );
		this->sendToHost( QString( itr.key().toStdString().c_str() ), arr_data );
		itr++;
	}
}

QString CheckController::post_http_header( QString jetmon_server, int content_size ) {
	QString ret_val = "POST /put/host-status";
	ret_val += " HTTP/1.1\r\nHost: ";
	ret_val += jetmon_server;
	ret_val += "\r\nContent-Type: application/json";
	ret_val += "\r\nContent-Length: " + QString::number( content_size );
	ret_val += "\r\nConnection: Keep-Alive\r\n\r\n";

	return ret_val;
}

bool CheckController::sendToHost( QString jetmon_server, QByteArray status_data ) {
	QDateTime timer;
	int response = -1;

	if ( m_debug ) {
		timer = QDateTime::currentDateTime();
	}

	if ( NULL == m_socket ) {
		m_socket = new QSslSocket();
		m_socket->setSslConfiguration( *m_ssl_config );
	}

	m_socket->connectToHostEncrypted( jetmon_server, m_jetmon_server_port );
	m_socket->waitForEncrypted();

	if ( m_socket->isEncrypted() ) {
		if ( m_debug ) {
			timer = QDateTime::currentDateTime();
			LOG( QString::number( timer.msecsTo( QDateTime::currentDateTime() ) ) +
				QString( "\t: SENDING :\tconnected to :" ) + jetmon_server );
			timer = QDateTime::currentDateTime();
		}

		m_socket->write( status_data );
		m_socket->flush();
		m_socket->waitForBytesWritten();

		if ( m_socket->waitForReadyRead() ) {
			response = this->readResponse();
		}
	}

	if ( m_debug ) {
		if ( 1 == response ) {
				LOG( QString::number( timer.msecsTo( QDateTime::currentDateTime() ) ) +
					QString( "\t: SENDING :\tsent - " ) + QString::number( response ) );
		} else {
				LOG( QString::number( timer.msecsTo( QDateTime::currentDateTime() ) ) +
					QString( "\t: SENDING :\tfailed to connect to :" ) + jetmon_server );
		}
	}

	if ( m_socket->isOpen() ) {
		m_socket->close();
	}

	return ( 1 == response );
}

QJsonDocument CheckController::parse_json_response( QByteArray &raw_data ) {
	QJsonDocument ret_val;
	QString s_data = raw_data.data();

	if ( ( -1 == s_data.indexOf( "{" ) ) || ( -1 == s_data.lastIndexOf( "}" ) ) ) {
		LOG( "Invalid JSON response format." );
		return ret_val;
	}

	s_data = s_data.mid( s_data.indexOf( "{" ), s_data.lastIndexOf( "}" ) - s_data.indexOf( "{" ) + 1 );
	ret_val = QJsonDocument::fromJson( s_data.toUtf8() );
	return ret_val;
}

int CheckController::readResponse() {
	QByteArray a_data = m_socket->readAll();

	if ( 0 == a_data.length() ) {
		LOG( "NO data returned when reading jetmon response." );
		return 0;
	}

	QJsonDocument json_doc = parse_json_response( a_data );

	if ( json_doc.isEmpty() || json_doc.isNull() ) {
		LOG( "Invalid JSON document format." );
		return 0;
	}

	QJsonValue response = json_doc.object().value( "response" );
	if ( response.isNull() ) {
		LOG( "Missing 'response' JSON value." );
		return 0;
	}

	if ( 1 != response.toInt() ) {
		LOG( QString( "Jetmon server FAILED to received the response: " ) + json_doc.toJson() );
	}

	return response.toInt();
}

