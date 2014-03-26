QT       += core network
QT       -= gui

TARGET = veriflier
CONFIG   += console
CONFIG   -= app_bundle

TEMPLATE = app

SOURCES += \
    source/client_thread.cpp \
    source/main.cpp \
    source/ssl_server.cpp \
    source/http_checker.cpp \
    source/config.cpp \
    source/logger.cpp

HEADERS += \
    headers/client_thread.h \
    headers/http_checker.h \
    headers/ssl_server.h \
    headers/config.h \
    headers/logger.h
