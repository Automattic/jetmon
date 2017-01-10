
#include "headers/check_thread.h"
#include "headers/logger.h"

CheckThread::CheckThread( const int net_timeout, const bool debug, const int thread_index )
	: QThread( 0 ), m_net_timeout( net_timeout ), m_thread_index( thread_index ), m_debug( debug )
{
	;
}

void CheckThread::run() {
	LOG( "checker #" + QString::number( m_thread_index ) + " running" );
	exec();
}

void CheckThread::finishedCheck( HTTP_Checker *checker, HealthCheck *hc ) {
	int status = -1;
	if ( checker->get_rtt() > 0 && 0 < checker->get_response_code() && 400 > checker->get_response_code() )
		status = HOST_ONLINE;
	else
		status = HOST_DOWN;

	if ( m_debug ) {
		LOG( QString::number( hc->received.msecsTo( QDateTime::currentDateTime() ) ) +
			QString( "\t: STAGE 2 :\t" ) + hc->monitor_url +
			QString( " status :" ) + QString::number( status ) );
	}

	emit resultReady( hc->thread_index, hc->blog_id, status, checker->get_response_code(), checker->get_rtt() );
	checker->deleteLater();
}

void CheckThread::performCheck( HealthCheck *hc ) {
	if ( m_debug ) {
		LOG( QString::number( hc->received.msecsTo( QDateTime::currentDateTime() ) ) +
			 QString( "  \t: STAGE 1 :\t" ) + QString( hc->monitor_url ) );
	}

	// Start the check on our side
	HTTP_Checker *m_checker = new HTTP_Checker( m_net_timeout );
	QObject::connect( m_checker, SIGNAL( finished( HTTP_Checker*, HealthCheck* ) ), this, SLOT( finishedCheck( HTTP_Checker*, HealthCheck* ) ) );
	m_checker->check( hc );
}

