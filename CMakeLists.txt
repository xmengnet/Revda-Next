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

find_program(GO_EXECUTABLE NAMES go REQUIRED)
if(${GO_EXECUTABLE} STREQUAL GO_EXECUTABLE-NOTFOUND)
    message(FATAL_ERROR "go compiler not found!!!")
endif()

include(FeatureSummary)

set(REVDA_BIN "${CMAKE_CURRENT_BINARY_DIR}/revda")
add_custom_command(
    OUTPUT "${REVDA_BIN}"
    COMMAND ${GO_EXECUTABLE} build -trimpath -ldflags "-s -w" -o "${REVDA_BIN}" "${CMAKE_CURRENT_SOURCE_DIR}/main.go"
    DEPENDS "${CMAKE_CURRENT_SOURCE_DIR}/main.go"
    WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}"
    COMMENT "Building revda (Go)"
)
add_custom_target(revda-bin ALL DEPENDS "${REVDA_BIN}")

string(TOUPPER "${CMAKE_BUILD_TYPE}" CMAKE_BUILD_TYPE_UPPER)
if(CMAKE_BUILD_TYPE_UPPER STREQUAL DEBUG)
    set(RUST_TARGET_DIR target/debug/)
    set(RUST_BUILD_FLAG)
else()
    set(RUST_TARGET_DIR target/release/)
    set(RUST_BUILD_FLAG --release)
endif()

if (NOT NODMLIVE)
    find_package(Cargo REQUIRED)
    find_package(Rust REQUIRED)
    set(DML_DIR "${CMAKE_CURRENT_SOURCE_DIR}/dmlive")
    set(DML_BIN "${DML_DIR}/${RUST_TARGET_DIR}/dmlive")

    add_custom_command(
        OUTPUT "${DML_BIN}"
        COMMAND ${Cargo_EXECUTABLE} build ${RUST_BUILD_FLAG}
        DEPENDS dmlive/src/main.rs
        WORKING_DIRECTORY "${DML_DIR}"
        COMMENT "Building dmlive (Rust)"
    )
    add_custom_target(dmlive-bin ALL DEPENDS "${DML_BIN}")
endif()

if (NOT NODMLIVE)
    install(PROGRAMS "${DML_BIN}" DESTINATION bin)
endif()
install(PROGRAMS "${REVDA_BIN}" DESTINATION bin)

install(FILES "icon/revda.svg"
    DESTINATION "share/icons/hicolor/scalable/apps")
install(FILES "misc/revda.desktop"
    DESTINATION "share/applications")
install(FILES "misc/dmlive-mime.desktop"
    DESTINATION "share/applications")
