cmake_minimum_required(VERSION 3.20)
project(weave_ctags_fixture)

function(configure_feature target)
  target_compile_definitions(${target} PRIVATE FEATURE_ENABLED=1)
endfunction()
