use std::{
    ffi::CString,
    fs, ptr,
    sync::atomic::{AtomicU64, Ordering},
};

use ahash::AHashMap;
use pig_tokenizer_native::ffi::{
    PIG_TOKENIZER_INVALID_ARGUMENT, PIG_TOKENIZER_LOAD_ERROR, PIG_TOKENIZER_OK,
    PigTokenizerCountHandle, pig_tokenizer_abi_version, pig_tokenizer_count,
    pig_tokenizer_count_destroy, pig_tokenizer_count_open,
};
use tokenizers::{Tokenizer, models::wordlevel::WordLevel, pre_tokenizers::whitespace::Whitespace};

static FIXTURE_SEQUENCE: AtomicU64 = AtomicU64::new(0);

#[test]
fn c_abi_counts_without_digest_context_or_analysis_handle() {
    let tokenizer_path = write_tokenizer_fixture();
    let tokenizer_path_c = CString::new(tokenizer_path.to_string_lossy().as_bytes()).unwrap();
    let mut counter_handle: *mut PigTokenizerCountHandle = ptr::null_mut();
    let mut error_buffer = [0_i8; 256];

    assert_eq!(pig_tokenizer_abi_version(), 3);
    assert_eq!(
        unsafe {
            pig_tokenizer_count_open(
                tokenizer_path_c.as_ptr(),
                &mut counter_handle,
                error_buffer.as_mut_ptr(),
                error_buffer.len(),
            )
        },
        PIG_TOKENIZER_OK,
        "open counter error = {error_buffer:?}"
    );
    assert!(!counter_handle.is_null());

    let input = b"hello world";
    let mut token_count = 0_u64;
    assert_eq!(
        unsafe {
            pig_tokenizer_count(
                counter_handle,
                input.as_ptr(),
                input.len(),
                0,
                &mut token_count,
                error_buffer.as_mut_ptr(),
                error_buffer.len(),
            )
        },
        PIG_TOKENIZER_OK,
        "count error = {error_buffer:?}"
    );
    assert_eq!(token_count, 2);

    assert_eq!(
        unsafe { pig_tokenizer_count_destroy(&mut counter_handle) },
        PIG_TOKENIZER_OK
    );
    assert!(counter_handle.is_null());
    assert_eq!(
        unsafe { pig_tokenizer_count_destroy(&mut counter_handle) },
        PIG_TOKENIZER_OK
    );

    fs::remove_file(tokenizer_path).unwrap();
}

#[test]
fn count_abi_rejects_invalid_boundaries_without_returning_a_count() {
    let tokenizer_path = write_tokenizer_fixture();
    let tokenizer_path_c = CString::new(tokenizer_path.to_string_lossy().as_bytes()).unwrap();
    let missing_path = tokenizer_path.with_extension("missing");
    let missing_path_c = CString::new(missing_path.to_string_lossy().as_bytes()).unwrap();
    let mut counter_handle: *mut PigTokenizerCountHandle = ptr::null_mut();
    let mut error_buffer = [0_i8; 256];

    assert_eq!(
        unsafe {
            pig_tokenizer_count_open(
                missing_path_c.as_ptr(),
                &mut counter_handle,
                error_buffer.as_mut_ptr(),
                error_buffer.len(),
            )
        },
        PIG_TOKENIZER_LOAD_ERROR
    );
    assert!(counter_handle.is_null());
    assert_eq!(
        unsafe {
            pig_tokenizer_count_open(
                tokenizer_path_c.as_ptr(),
                ptr::null_mut(),
                error_buffer.as_mut_ptr(),
                error_buffer.len(),
            )
        },
        PIG_TOKENIZER_INVALID_ARGUMENT
    );
    assert_eq!(
        unsafe {
            pig_tokenizer_count_open(
                tokenizer_path_c.as_ptr(),
                &mut counter_handle,
                error_buffer.as_mut_ptr(),
                error_buffer.len(),
            )
        },
        PIG_TOKENIZER_OK
    );

    let valid_input = b"hello";
    let invalid_utf8 = [0xff_u8];
    let mut token_count = 99_u64;
    assert_eq!(
        unsafe {
            pig_tokenizer_count(
                counter_handle,
                invalid_utf8.as_ptr(),
                invalid_utf8.len(),
                0,
                &mut token_count,
                error_buffer.as_mut_ptr(),
                error_buffer.len(),
            )
        },
        PIG_TOKENIZER_INVALID_ARGUMENT
    );
    assert_eq!(token_count, 0);

    token_count = 99;
    assert_eq!(
        unsafe {
            pig_tokenizer_count(
                counter_handle,
                valid_input.as_ptr(),
                valid_input.len(),
                2,
                &mut token_count,
                error_buffer.as_mut_ptr(),
                error_buffer.len(),
            )
        },
        PIG_TOKENIZER_INVALID_ARGUMENT
    );
    assert_eq!(token_count, 0);

    token_count = 99;
    assert_eq!(
        unsafe {
            pig_tokenizer_count(
                counter_handle,
                ptr::null(),
                1,
                0,
                &mut token_count,
                error_buffer.as_mut_ptr(),
                error_buffer.len(),
            )
        },
        PIG_TOKENIZER_INVALID_ARGUMENT
    );
    assert_eq!(token_count, 0);

    assert_eq!(
        unsafe {
            pig_tokenizer_count(
                counter_handle,
                valid_input.as_ptr(),
                valid_input.len(),
                0,
                ptr::null_mut(),
                error_buffer.as_mut_ptr(),
                error_buffer.len(),
            )
        },
        PIG_TOKENIZER_INVALID_ARGUMENT
    );
    assert_eq!(
        unsafe {
            pig_tokenizer_count(
                ptr::null_mut(),
                valid_input.as_ptr(),
                valid_input.len(),
                0,
                &mut token_count,
                error_buffer.as_mut_ptr(),
                error_buffer.len(),
            )
        },
        PIG_TOKENIZER_INVALID_ARGUMENT
    );
    assert_eq!(
        unsafe { pig_tokenizer_count_destroy(&mut counter_handle) },
        PIG_TOKENIZER_OK
    );
    assert_eq!(
        unsafe { pig_tokenizer_count_destroy(ptr::null_mut()) },
        PIG_TOKENIZER_INVALID_ARGUMENT
    );

    fs::remove_file(tokenizer_path).unwrap();
}

fn write_tokenizer_fixture() -> std::path::PathBuf {
    let vocabulary = AHashMap::from([
        ("[UNK]".to_string(), 0),
        ("hello".to_string(), 1),
        ("world".to_string(), 2),
    ]);
    let model = WordLevel::builder()
        .vocab(vocabulary)
        .unk_token("[UNK]".to_string())
        .build()
        .unwrap();
    let mut tokenizer = Tokenizer::new(model);
    tokenizer.with_pre_tokenizer(Some(Whitespace));
    let sequence = FIXTURE_SEQUENCE.fetch_add(1, Ordering::Relaxed);
    let path = std::env::temp_dir().join(format!(
        "pig-tokenizer-ffi-{}-{sequence}.json",
        std::process::id()
    ));
    tokenizer.save(&path, false).unwrap();
    path
}
