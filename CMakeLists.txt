cmake_minimum_required(VERSION 3.6)
# SET(CMAKE_INSTALL_PREFIX /usr CACHE PATH "Set install prefix.")
project(revda)
set(PROJECT_VERSION "5.7.0")

set(CMAKE_INCLUDE_CURRENT_DIR ON)

find_package(ECM 1.0 QUIET)
if (ECM_FOUND)
    set(CMAKE_MODULE_PATH ${ECM_MODULE_PATH} ${ECM_KDE_MODULE_DIR} ${CMAKE_CURRENT_SOURCE_DIR}/cmake)
    include(ECMUninstallTarget)
else()
    set(CMAKE_MODULE_PATH ${CMAKE_CURRENT_SOURCE_DIR}/cmake)
endif()

find_program(MPV_PATH NAMES mpv REQUIRED)
if(${MPV_PATH} STREQUAL MPV_PATH-NOTFOUND)
    message(FATAL_ERROR "mpv not found!!!")
endif()

# Runtime dependency of both revda and dmlive: the stream relay, recording
# and the danmaku post-mux all shell out to it.
find_program(FFMPEG_PATH NAMES ffmpeg REQUIRED)
if(${FFMPEG_PATH} STREQUAL FFMPEG_PATH-NOTFOUND)
    message(FATAL_ERROR "ffmpeg not found!!!")
endif()

find_program(GO_EXECUTABLE NAMES go REQUIRED)
if(${GO_EXECUTABLE} STREQUAL GO_EXECUTABLE-NOTFOUND)
    message(FATAL_ERROR "go compiler not found!!!")
endif()

include(FeatureSummary)

file(GLOB_RECURSE REVDA_GO_SOURCES "${CMAKE_CURRENT_SOURCE_DIR}/*.go")

set(REVDA_BIN "${CMAKE_CURRENT_BINARY_DIR}/revda")
add_custom_command(
    OUTPUT "${REVDA_BIN}"
    COMMAND ${GO_EXECUTABLE} build -trimpath -ldflags "-s -w" -o "${REVDA_BIN}" "${CMAKE_CURRENT_SOURCE_DIR}/main.go"
    DEPENDS ${REVDA_GO_SOURCES}
    WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}"
    COMMENT "Building revda (Go)"
)
add_custom_target(revda-bin ALL DEPENDS "${REVDA_BIN}")

# Like the Rust packaging, dmlive is built and installed to bin by default;
# pass -DNODMLIVE to skip it. It is now the pure-Go CLI (cmd/dmlive) — the
# GUI embeds the same engine as a library, so nothing depends on this binary
# at runtime except the dmlive:// scheme handler and terminal use.
if (NOT NODMLIVE)
    set(DMLIVE_BIN "${CMAKE_CURRENT_BINARY_DIR}/dmlive")
    add_custom_command(
        OUTPUT "${DMLIVE_BIN}"
        COMMAND ${GO_EXECUTABLE} build -trimpath -ldflags "-s -w" -o "${DMLIVE_BIN}" "${CMAKE_CURRENT_SOURCE_DIR}/cmd/dmlive"
        DEPENDS ${REVDA_GO_SOURCES}
        WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}"
        COMMENT "Building dmlive (Go)"
    )
    add_custom_target(dmlive-bin ALL DEPENDS "${DMLIVE_BIN}")
endif()

if (NOT NODMLIVE)
    install(PROGRAMS "${DMLIVE_BIN}" DESTINATION bin)
endif()
install(PROGRAMS "${REVDA_BIN}" DESTINATION bin)

install(FILES "icon/revda.svg"
    DESTINATION "share/icons/hicolor/scalable/apps")
install(FILES "misc/revda.desktop"
    DESTINATION "share/applications")
install(FILES "misc/dmlive-mime.desktop"
    DESTINATION "share/applications")
