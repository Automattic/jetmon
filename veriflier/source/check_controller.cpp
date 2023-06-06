
#include "headers/check_controller.h"
#include "headers/logger.h"

#include <QMutexLocker>
#include <QJsonDocument>
#include <QJsonArray>

CheckController::CheckController( const QSslConfiguration *ssl_config, const int jetmon_server_port,
								  const int max_runners, const int max_checks, const QString &veriflier_name,
								  const QString &auth_token, const int net_timeout, const bool debug )
	: m_ssl_config( ssl_config ), m_jetmon_server_port( jetmon_server_port ), m_socket( NULL ),
	m_max_checkers( max_runners ), m_max_checks( max_checks ), m_checking( 0 ), m_checked( 0 ),
	m_veriflier_name( veriflier_name ), m_auth_token( auth_token ), m_net_timeout( net_timeout ), m_debug( debug )
{
	m_checks.resize( 0 );
	m_ticker = new QTimer( this );
	connect( m_ticker, SIGNAL( timeout() ), this, SLOT( ticked() ) );
	m_ticker->start( 5000 );

	for ( int thread_index = 0; thread_index < m_max_checkers; thread_index++ ) {
		CheckThread *ct = new CheckThread( m_net_timeout, m_debug, thread_index );
		connect( ct, SIGNAL( resultReady(int, qint64, QString, int, int, int) ), this, SLOT( finishedChecking(int, qint64, QString, int, int, int) ) );
		Runner* run = new Runner();
		run->ct = ct;
		run->checking = 0;
		run->ct->start();
		m_runners.push_back(run);
	}
	connect( this, SIGNAL(startCheck(HealthCheck*)), this, SLOT(startChecking(HealthCheck*)) );
}

CheckController::~CheckController() {
	m_ticker->stop();
	delete m_ticker;
	delete m_socket;
	for ( int checker = 0; checker < m_runners.length(); checker++ ) {
		delete m_runners[checker]->ct;
		m_runners[checker]->ct = NULL;
	}
	qDeleteAll(m_runners);
}

void CheckController::finishedChecking( int thread_index, qint64 blog_id, QString monitor_url, int status, int http_code, int rtt ) {
	QJsonDocument json_doc;
	QJsonObject json_obj, arr_result;
	QJsonArray checkArray;
	m_checked++;
	m_check_lock.lock();
	for ( int loop = 0; loop < m_checks.size(); loop++ ) {
		if ( m_checks[loop]->blog_id == blog_id && m_checks[loop]->monitor_url == monitor_url ) {
			if ( 0 > m_checks[loop]->thread_index ) {
				LOG( "deleting a blog_id that does not have a check thread assigned?: " + QString::number( blog_id ) + " " + monitor_url );
			}
			if ( thread_index != m_checks[loop]->thread_index ) {
				LOG( "deleting a blog_id that has a different thread_index linked: " +
					 QString::number( m_checks[loop]->thread_index  )  + " != " + QString::number( thread_index ) );
			}
			arr_result.insert( "blog_id", QJsonValue( blog_id ) );
			arr_result.insert( "monitor_url", QJsonValue( monitor_url ) );
			arr_result.insert( "status", QJsonValue( status ) );
			arr_result.insert( "code", QJsonValue( http_code ) );
			arr_result.insert( "rtt", QJsonValue( rtt ) );
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

			HealthCheck *ptr = m_checks[loop];
			m_checks.remove( loop );
			delete ptr;
			m_runners[thread_index]->checking--;
			m_checking--;
			break;
		}
	}
	if ( m_checking < ( m_runners.length() * m_max_checks ) ) {
		for ( int loop = 0; loop < m_checks.size(); loop++ ) {
			if ( NOT_ASSIGNED == m_checks[loop]->thread_index ) {
				m_checks[loop]->thread_index = PRE_ASSIGNED;
				emit startCheck( m_checks[loop] );
				m_check_lock.unlock();
				return;
			}
		}
	}
	m_check_lock.unlock();
}

inline bool CheckController::haveCheck( qint64 blog_id, QString monitor_url ) {
	for ( int loop = 0; loop < m_checks.size(); loop++ ) {
		if ( m_checks[loop]->blog_id == blog_id && m_checks[loop]->monitor_url == monitor_url ) {
			return true;
		}
	}
	return false;
}

void CheckController::startChecking( HealthCheck* hc ) {
	m_checking++;
	int runner = this->selectRunner();
	m_runners[runner]->checking++;
	hc->thread_index = runner;
	m_runners[runner]->ct->performCheck( hc );
}

int CheckController::selectRunner() {
	int min = m_max_checks;
	int min_index = 0;
	for ( int index = 0; index < m_runners.length(); index++ ) {
		if ( m_runners[index]->checking < min ) {
			min = m_runners[index]->checking;
			min_index = index;
		}
	}
	return min_index;
}

void CheckController::addCheck( HealthCheck* hc ) {
	if ( haveCheck( hc->blog_id, hc->monitor_url ) ) {
		LOG( "ERROR:\t: already have this blog in the check list: " + QString::number( hc->blog_id ) + " " + hc->monitor_url );
		return;
	}

	m_checks.append( hc );
	if ( m_checking < ( m_runners.length() * m_max_checks ) ) {
		hc->thread_index = PRE_ASSIGNED;
		emit startCheck( hc );
	}
}

void CheckController::addChecks( QVector<HealthCheck *> hcs ) {
	m_check_lock.lock();
	for ( int loop = 0; loop < hcs.size(); loop++ ) {
		this->addCheck( hcs[loop] );
	}
	m_check_lock.unlock();
}

void CheckController::ticked() {
	this->sendResults();

	if ( m_checks.size() > 0 || m_checked > 0 ) {
		LOG( "total - " + QString::number( m_checks.size() ) +
			" : checking - " + QString::number( m_checking ) +
			" : checked = " + QString::number( m_checked ) );
		for ( int index = 0; index < m_runners.length(); index++ ) {
			LOG( "runner " + QString::number( index ) + "\t: checking " + QString::number( m_runners[index]->checking ) );
		}
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
		LOG( "\t\t: SENDING :\t" + QString::number( itr.value().object()["checks"].toArray().size() ) + " results" );
		this->sendToJetmonServer( QString( itr.key().toStdString().c_str() ), arr_data );
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

void CheckController::sendToJetmonServer( QString jetmon_server, QByteArray status_data ) {
	JetmonServer * js = new JetmonServer( this, m_ssl_config, jetmon_server, m_jetmon_server_port );
	QObject::connect( js, SIGNAL( finished( JetmonServer*, int, int ) ), SLOT( finishedSending( JetmonServer*, int, int ) ) );
	js->sendData( status_data );
}

void CheckController::finishedSending( JetmonServer* js, int status, int rtt ) {
	if ( 1 == status ) {
		LOG( QString::number( rtt ) + "\t\t: SENDING :\tsent - 1" );
	} else {
		LOG( QString::number( rtt ) + "\t\t: SENDING :\tfailed to connect to :" + js->jetmonServer() );
	}
	js->deleteLater();
}
