
#ifndef __LOGGER_H__
#define __LOGGER_H__

#include <QDir>
#include <QFile>
#include <QMutex>
#include <QDateTime>

#include <iostream>

#define MAX_LOG_FILESIZE 1024 * 1024 * 10 // 10 MB

const QString LOG_FILE_NAME = QDir::currentPath() + "/logs/veriflier.log";
#define LOG( content ) Logger::write( content )

class Logger {
public:
	static Logger* instance() { return m_instance; }

	static void startLogger();
	static void stopLogging();
	static void write( QString s_data );

private:
	Logger() {}
	static Logger *m_instance;
	static QFile *m_file;
	static QMutex *m_mutex;

	static void do_log_rotation();
};

#endif // __CONFIG_H__

