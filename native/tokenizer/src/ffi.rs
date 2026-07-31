use std::{
    ffi::{CStr, c_char},
    panic::{AssertUnwindSafe, catch_unwind},
    ptr, slice, str,
};

use crate::NativeTokenizer;

pub const PIG_TOKENIZER_OK: i32 = 0;
pub const PIG_TOKENIZER_INVALID_ARGUMENT: i32 = 1;
pub const PIG_TOKENIZER_LOAD_ERROR: i32 = 2;
pub const PIG_TOKENIZER_PANIC: i32 = 4;
pub const PIG_TOKENIZER_COUNT_ERROR: i32 = 5;

#[repr(C)]
pub struct PigTokenizerCountHandle {
    tokenizer: NativeTokenizer,
}

struct FfiError {
    code: i32,
    message: String,
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_count_open(
    tokenizer_path: *const c_char,
    out_handle: *mut *mut PigTokenizerCountHandle,
    error_buffer: *mut c_char,
    error_capacity: usize,
) -> i32 {
    ffi_boundary(error_buffer, error_capacity, || {
        if out_handle.is_null() {
            return Err(invalid_argument("counter output handle is required"));
        }
        unsafe { out_handle.write(ptr::null_mut()) };
        let tokenizer_path = unsafe { required_c_string(tokenizer_path, "tokenizer path") }?;
        let tokenizer = NativeTokenizer::from_file(tokenizer_path)
            .map_err(|error| FfiError::new(PIG_TOKENIZER_LOAD_ERROR, error.to_string()))?;
        let handle = Box::new(PigTokenizerCountHandle { tokenizer });
        unsafe { out_handle.write(Box::into_raw(handle)) };
        Ok(())
    })
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_count(
    handle: *mut PigTokenizerCountHandle,
    input: *const u8,
    input_len: usize,
    add_special_tokens: u8,
    out_token_count: *mut u64,
    error_buffer: *mut c_char,
    error_capacity: usize,
) -> i32 {
    ffi_boundary(error_buffer, error_capacity, || {
        if handle.is_null() {
            return Err(invalid_argument("counter handle is required"));
        }
        if out_token_count.is_null() {
            return Err(invalid_argument("token count output is required"));
        }
        unsafe { out_token_count.write(0) };
        let input = unsafe { required_bytes(input, input_len, "rendered input") }?;
        let input = str::from_utf8(input)
            .map_err(|_| invalid_argument("rendered input must be valid UTF-8"))?;
        let add_special_tokens = parse_add_special_tokens(add_special_tokens)?;
        let handle = unsafe { &*handle };
        let count = handle
            .tokenizer
            .count(input, add_special_tokens)
            .map_err(|error| FfiError::new(PIG_TOKENIZER_COUNT_ERROR, error.to_string()))?;
        let count = u64::try_from(count).map_err(|_| {
            FfiError::new(PIG_TOKENIZER_COUNT_ERROR, "token count exceeds ABI range")
        })?;
        unsafe { out_token_count.write(count) };
        Ok(())
    })
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pig_tokenizer_count_destroy(
    handle: *mut *mut PigTokenizerCountHandle,
) -> i32 {
    status_boundary(|| {
        if handle.is_null() {
            return Err(invalid_argument("counter handle pointer is required"));
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
    3
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

fn parse_add_special_tokens(value: u8) -> Result<bool, FfiError> {
    match value {
        0 => Ok(false),
        1 => Ok(true),
        _ => Err(invalid_argument("add-special-tokens flag must be 0 or 1")),
    }
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
