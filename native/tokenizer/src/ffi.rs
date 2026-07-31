use std::ffi::c_char;

pub const PIG_TOKENIZER_OK: i32 = 0;
pub const PIG_TOKENIZER_UNAVAILABLE: i32 = 9;

#[repr(C)]
pub struct PigTokenizerHandle {
    _private: [u8; 0],
}

#[repr(C)]
pub struct PigTokenizationAnalysisHandle {
    _private: [u8; 0],
}

#[repr(C)]
#[derive(Clone, Copy, Debug)]
pub struct PigTokenizationAnalysisView {
    pub token_count: u64,
    pub full_block_count: u64,
    pub full_block_digests: *const u8,
    pub partial_block_tokens: u64,
    pub partial_block_digest: [u8; 32],
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_open(
    _tokenizer_path: *const c_char,
    _manifest_id: *const c_char,
    _backend_epoch: *const c_char,
    _block_size: u64,
    _key: *const u8,
    _key_len: usize,
    _out_handle: *mut *mut PigTokenizerHandle,
    _error_buffer: *mut c_char,
    _error_capacity: usize,
) -> i32 {
    PIG_TOKENIZER_UNAVAILABLE
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_analyze(
    _handle: *mut PigTokenizerHandle,
    _input: *const u8,
    _input_len: usize,
    _add_special_tokens: u8,
    _out_analysis: *mut *mut PigTokenizationAnalysisHandle,
    _error_buffer: *mut c_char,
    _error_capacity: usize,
) -> i32 {
    PIG_TOKENIZER_UNAVAILABLE
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_analysis_view(
    _analysis: *const PigTokenizationAnalysisHandle,
    _out_view: *mut PigTokenizationAnalysisView,
) -> i32 {
    PIG_TOKENIZER_UNAVAILABLE
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_analysis_destroy(
    _analysis: *mut *mut PigTokenizationAnalysisHandle,
) -> i32 {
    PIG_TOKENIZER_UNAVAILABLE
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_destroy(
    _handle: *mut *mut PigTokenizerHandle,
) -> i32 {
    PIG_TOKENIZER_UNAVAILABLE
}

#[unsafe(no_mangle)]
pub extern "C" fn pig_tokenizer_abi_version() -> u32 {
    1
}
