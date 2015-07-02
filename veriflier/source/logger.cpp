
#include "headers/logger.h"

Logger *Logger::m_instance = new Logger;
QFile *Logger::m_file = new QFile;
QMutex *Logger::m_mutex = new QMutex;

void Logger::stopLogging() {
	m_file->close();
}

void Logger::startLogger() {
	QDir check;
	if ( ! check.exists( QDir::currentPath() + "/logs" ) )
		check.mkdir( QDir::currentPath() + "/logs" );
	m_file->setFileName( LOG_FILE_NAME );
	m_file->open( QIODevice::WriteOnly | QIODevice::Text | QIODevice::Append );
}

void Logger::write( QString s_data ) {
	m_mutex->lock();
	if ( ( QFile( LOG_FILE_NAME ).size() ) > MAX_LOG_FILESIZE ) {
		m_file->close();
		Logger::do_log_rotation();
		m_file->setFileName( LOG_FILE_NAME );
		m_file->open( QIODevice::WriteOnly | QIODevice::Text | QIODevice::Append );
	}
	if ( m_file->isOpen() ) {
		m_file->write( QDateTime::currentDateTime().toString( "yyyy-MM-dd hh:mm:ss").toStdString().c_str() );
		m_file->write( " - " );
		m_file->write( s_data.toStdString().c_str() );
		m_file->write( "\n" );
		m_file->flush();
	}
	m_mutex->unlock();
}

void Logger::do_log_rotation() {
	for ( int del_loop = ( LOGS_TO_KEEP - 1 ); del_loop > 0; del_loop-- ) {
		if ( QFile( LOG_FILE_NAME + "." + QString::number( del_loop ) ).exists() ) {
			if ( QFile( LOG_FILE_NAME +"." + QString::number( del_loop + 1 ) ).exists() )
				QFile( LOG_FILE_NAME + "." + QString::number( del_loop + 1 ) ).remove();
			QFile( LOG_FILE_NAME + "." + QString::number( del_loop ) ).copy(
					LOG_FILE_NAME + "." + QString::number( del_loop + 1 ) );
		}
	}
	if ( QFile( LOG_FILE_NAME + ".1" ).exists() )
		QFile( LOG_FILE_NAME + ".1" ).remove();
	QFile( LOG_FILE_NAME ).copy( LOG_FILE_NAME + ".1" );
	QFile( LOG_FILE_NAME ).remove();
}

