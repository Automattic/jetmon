
#include "headers/check_thread.h"
#include "headers/logger.h"

CheckThread::CheckThread( const QSslConfiguration *ssl_config,
						const int net_timeout, const bool debug )
	: m_socket( NULL ), m_ssl_config( ssl_config ),
	m_net_timeout( net_timeout ), m_debug( debug )
{
	connect( this, SIGNAL( finished() ), this, SLOT( deleteLater() ) );
}

void CheckThread::run() {
	if ( m_debug ) {
		LOG( QString::number( m_timer.msecsTo( QDateTime::currentDateTime() ) ) +
			 QString( "  \t: STAGE 1 :\t" ) + QString( m_monitor_url ) );
	}

	// Start the check on our side
	this->performHostCheck();
	emit resultReady( m_blog_id, m_status );
}

void CheckThread::performHostCheck() {
	HTTP_Checker *http_check = new HTTP_Checker( m_net_timeout );
	http_check->check( m_monitor_url );

	if ( http_check->get_rtt() > 0 && 400 > http_check->get_response_code() )
		m_status = HOST_ONLINE;
	else
		m_status = HOST_DOWN;

	if ( m_debug ) {
		LOG( QString::number( m_timer.msecsTo( QDateTime::currentDateTime() ) ) +
			QString( "\t: STAGE 2 :\t" ) + m_monitor_url +
			QString( " status :" ) + QString::number( m_status ) );
	}

	delete http_check;
}

