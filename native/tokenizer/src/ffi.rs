use std::{
    ffi::{CStr, c_char},
    panic::{AssertUnwindSafe, catch_unwind},
    ptr, slice, str,
};

use crate::{BlockDigestContext, NativeTokenizer, TokenizationAnalysis};

pub const PIG_TOKENIZER_OK: i32 = 0;
pub const PIG_TOKENIZER_INVALID_ARGUMENT: i32 = 1;
pub const PIG_TOKENIZER_LOAD_ERROR: i32 = 2;
pub const PIG_TOKENIZER_ANALYSIS_ERROR: i32 = 3;
pub const PIG_TOKENIZER_PANIC: i32 = 4;

#[repr(C)]
pub struct PigTokenizerHandle {
    tokenizer: NativeTokenizer,
    digest_context: BlockDigestContext,
}

#[repr(C)]
pub struct PigTokenizationAnalysisHandle {
    analysis: TokenizationAnalysis,
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

struct FfiError {
    code: i32,
    message: String,
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_open(
    tokenizer_path: *const c_char,
    manifest_id: *const c_char,
    backend_epoch: *const c_char,
    block_size: u64,
    key: *const u8,
    key_len: usize,
    out_handle: *mut *mut PigTokenizerHandle,
    error_buffer: *mut c_char,
    error_capacity: usize,
) -> i32 {
    ffi_boundary(error_buffer, error_capacity, || {
        if out_handle.is_null() {
            return Err(invalid_argument("tokenizer output handle is required"));
        }
        unsafe { out_handle.write(ptr::null_mut()) };
        let tokenizer_path = unsafe { required_c_string(tokenizer_path, "tokenizer path") }?;
        let manifest_id = unsafe { required_c_string(manifest_id, "manifest id") }?;
        let backend_epoch = unsafe { required_c_string(backend_epoch, "backend epoch") }?;
        let block_size = usize::try_from(block_size)
            .ok()
            .filter(|value| *value > 0)
            .ok_or_else(|| invalid_argument("block size is invalid"))?;
        if key.is_null() || key_len != 32 {
            return Err(invalid_argument("analysis key must contain exactly 32 bytes"));
        }
        let key = unsafe { slice::from_raw_parts(key, key_len) };
        let digest_context = BlockDigestContext::new(
            manifest_id,
            backend_epoch,
            block_size,
            key,
        )
        .map_err(|error| invalid_argument(error.to_string()))?;
        let tokenizer = NativeTokenizer::from_file(tokenizer_path)
            .map_err(|error| FfiError::new(PIG_TOKENIZER_LOAD_ERROR, error.to_string()))?;
        let handle = Box::new(PigTokenizerHandle {
            tokenizer,
            digest_context,
        });
        unsafe { out_handle.write(Box::into_raw(handle)) };
        Ok(())
    })
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_analyze(
    handle: *mut PigTokenizerHandle,
    input: *const u8,
    input_len: usize,
    add_special_tokens: u8,
    out_analysis: *mut *mut PigTokenizationAnalysisHandle,
    error_buffer: *mut c_char,
    error_capacity: usize,
) -> i32 {
    ffi_boundary(error_buffer, error_capacity, || {
        if handle.is_null() {
            return Err(invalid_argument("tokenizer handle is required"));
        }
        if out_analysis.is_null() {
            return Err(invalid_argument("analysis output handle is required"));
        }
        unsafe { out_analysis.write(ptr::null_mut()) };
        let input = unsafe { required_bytes(input, input_len, "rendered input") }?;
        let input = str::from_utf8(input)
            .map_err(|_| invalid_argument("rendered input must be valid UTF-8"))?;
        let add_special_tokens = match add_special_tokens {
            0 => false,
            1 => true,
            _ => return Err(invalid_argument("add-special-tokens flag must be 0 or 1")),
        };
        let handle = unsafe { &*handle };
        let analysis = handle
            .tokenizer
            .analyze(
                input,
                add_special_tokens,
                &handle.digest_context,
                false,
            )
            .map_err(|error| FfiError::new(PIG_TOKENIZER_ANALYSIS_ERROR, error.to_string()))?;
        let analysis = Box::new(PigTokenizationAnalysisHandle { analysis });
        unsafe { out_analysis.write(Box::into_raw(analysis)) };
        Ok(())
    })
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_analysis_view(
    analysis: *const PigTokenizationAnalysisHandle,
    out_view: *mut PigTokenizationAnalysisView,
) -> i32 {
    status_boundary(|| {
        if analysis.is_null() || out_view.is_null() {
            return Err(invalid_argument("analysis handle and output view are required"));
        }
        let analysis = unsafe { &(*analysis).analysis };
        let token_count = u64::try_from(analysis.token_count)
            .map_err(|_| invalid_argument("token count exceeds ABI range"))?;
        let full_block_count = u64::try_from(analysis.full_block_digests.len())
            .map_err(|_| invalid_argument("full block count exceeds ABI range"))?;
        let full_block_digests = if analysis.full_block_digests.is_empty() {
            ptr::null()
        } else {
            analysis.full_block_digests.as_ptr().cast::<u8>()
        };
        let (partial_block_tokens, partial_block_digest) = analysis
            .partial_block
            .as_ref()
            .map(|partial| {
                (
                    u64::try_from(partial.token_count).unwrap_or(u64::MAX),
                    partial.digest,
                )
            })
            .unwrap_or((0, [0; 32]));
        if partial_block_tokens == u64::MAX {
            return Err(invalid_argument("partial block count exceeds ABI range"));
        }
        unsafe {
            out_view.write(PigTokenizationAnalysisView {
                token_count,
                full_block_count,
                full_block_digests,
                partial_block_tokens,
                partial_block_digest,
            })
        };
        Ok(())
    })
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_analysis_destroy(
    analysis: *mut *mut PigTokenizationAnalysisHandle,
) -> i32 {
    status_boundary(|| {
        if analysis.is_null() {
            return Err(invalid_argument("analysis handle pointer is required"));
        }
        let owned = unsafe { analysis.read() };
        if !owned.is_null() {
            unsafe { drop(Box::from_raw(owned)) };
            unsafe { analysis.write(ptr::null_mut()) };
        }
        Ok(())
    })
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_destroy(
    handle: *mut *mut PigTokenizerHandle,
) -> i32 {
    status_boundary(|| {
        if handle.is_null() {
            return Err(invalid_argument("tokenizer handle pointer is required"));
        }
        let owned = unsafe { handle.read() };
        if !owned.is_null() {
            unsafe { drop(Box::from_raw(owned)) };
            unsafe { handle.write(ptr::null_mut()) };
        }
        Ok(())
    })
}

#[unsafe(no_mangle)]
pub extern "C" fn pig_tokenizer_abi_version() -> u32 {
    1
}

impl FfiError {
    fn new(code: i32, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
        }
    }
}

fn invalid_argument(message: impl Into<String>) -> FfiError {
    FfiError::new(PIG_TOKENIZER_INVALID_ARGUMENT, message)
}

fn ffi_boundary(
    error_buffer: *mut c_char,
    error_capacity: usize,
    operation: impl FnOnce() -> Result<(), FfiError>,
) -> i32 {
    write_error(error_buffer, error_capacity, "");
    match catch_unwind(AssertUnwindSafe(operation)) {
        Ok(Ok(())) => PIG_TOKENIZER_OK,
        Ok(Err(error)) => {
            write_error(error_buffer, error_capacity, &error.message);
            error.code
        }
        Err(_) => {
            write_error(error_buffer, error_capacity, "native tokenizer panicked");
            PIG_TOKENIZER_PANIC
        }
    }
}

fn status_boundary(operation: impl FnOnce() -> Result<(), FfiError>) -> i32 {
    match catch_unwind(AssertUnwindSafe(operation)) {
        Ok(Ok(())) => PIG_TOKENIZER_OK,
        Ok(Err(error)) => error.code,
        Err(_) => PIG_TOKENIZER_PANIC,
    }
}

unsafe fn required_c_string(pointer: *const c_char, name: &str) -> Result<String, FfiError> {
    if pointer.is_null() {
        return Err(invalid_argument(format!("{name} is required")));
    }
    let value = unsafe { CStr::from_ptr(pointer) }
        .to_str()
        .map_err(|_| invalid_argument(format!("{name} must be valid UTF-8")))?;
    if value.is_empty() {
        return Err(invalid_argument(format!("{name} is required")));
    }
    Ok(value.to_owned())
}

unsafe fn required_bytes<'a>(
    pointer: *const u8,
    length: usize,
    name: &str,
) -> Result<&'a [u8], FfiError> {
    if length == 0 {
        return Ok(&[]);
    }
    if pointer.is_null() {
        return Err(invalid_argument(format!("{name} pointer is required")));
    }
    Ok(unsafe { slice::from_raw_parts(pointer, length) })
}

fn write_error(buffer: *mut c_char, capacity: usize, message: &str) {
    if buffer.is_null() || capacity == 0 {
        return;
    }
    let bytes = message.as_bytes();
    let copy_length = bytes.len().min(capacity - 1);
    unsafe {
        for (index, value) in bytes.iter().take(copy_length).enumerate() {
            buffer.add(index).write(*value as c_char);
        }
        buffer.add(copy_length).write(0);
    }
}
