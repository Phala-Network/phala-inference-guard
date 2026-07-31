use std::{
    env,
    error::Error,
    io::{self, Read},
    process,
    time::Instant,
};

use pig_tokenizer_native::NativeTokenizer;

fn main() {
    if let Err(error) = run() {
        eprintln!("pig-tokenizer: {error}");
        process::exit(1);
    }
}

fn run() -> Result<(), Box<dyn Error>> {
    let arguments: Vec<String> = env::args().collect();
    if arguments.len() < 4 {
        return Err("usage: pig-tokenizer <encode|bench> <tokenizer.json> <add-special-tokens> [warmup iterations]".into());
    }
    let mode = arguments[1].as_str();
    let tokenizer_path = &arguments[2];
    let add_special_tokens = parse_bool(&arguments[3])?;
    let mut input = String::new();
    io::stdin().read_to_string(&mut input)?;

    let load_started = Instant::now();
    let tokenizer = NativeTokenizer::from_file(tokenizer_path)?;
    let load_micros = load_started.elapsed().as_secs_f64() * 1_000_000.0;
    match mode {
        "encode" => encode(&tokenizer, &input, add_special_tokens),
        "bench" => {
            if arguments.len() != 6 {
                return Err("bench requires warmup and iteration counts".into());
            }
            let warmup: usize = arguments[4].parse()?;
            let iterations: usize = arguments[5].parse()?;
            bench(
                &tokenizer,
                &input,
                add_special_tokens,
                warmup,
                iterations,
                load_micros,
            )
        }
        _ => Err(format!("unknown mode {mode:?}").into()),
    }
}

fn encode(
    tokenizer: &NativeTokenizer,
    input: &str,
    add_special_tokens: bool,
) -> Result<(), Box<dyn Error>> {
    let token_ids = tokenizer.encode(input, add_special_tokens)?;
    print!("[");
    for (index, token_id) in token_ids.iter().enumerate() {
        if index > 0 {
            print!(",");
        }
        print!("{token_id}");
    }
    println!("]");
    Ok(())
}

fn bench(
    tokenizer: &NativeTokenizer,
    input: &str,
    add_special_tokens: bool,
    warmup: usize,
    iterations: usize,
    load_micros: f64,
) -> Result<(), Box<dyn Error>> {
    if iterations == 0 {
        return Err("bench iterations must be positive".into());
    }
    for _ in 0..warmup {
        tokenizer.encode(input, add_special_tokens)?;
    }
    let mut durations = Vec::with_capacity(iterations);
    let mut token_count = 0;
    for _ in 0..iterations {
        let started = Instant::now();
        token_count = tokenizer.encode(input, add_special_tokens)?.len();
        durations.push(started.elapsed().as_secs_f64() * 1_000_000.0);
    }
    durations.sort_by(f64::total_cmp);
    println!(
        "{{\"input_bytes\":{},\"tokens\":{},\"warmup\":{},\"iterations\":{},\"load_us\":{:.3},\"p50_us\":{:.3},\"p95_us\":{:.3},\"p99_us\":{:.3},\"max_us\":{:.3}}}",
        input.len(),
        token_count,
        warmup,
        iterations,
        load_micros,
        percentile(&durations, 0.50),
        percentile(&durations, 0.95),
        percentile(&durations, 0.99),
        durations[durations.len() - 1],
    );
    Ok(())
}

fn percentile(sorted: &[f64], quantile: f64) -> f64 {
    let rank = (quantile * sorted.len() as f64).ceil() as usize;
    sorted[rank.saturating_sub(1).min(sorted.len() - 1)]
}

fn parse_bool(value: &str) -> Result<bool, Box<dyn Error>> {
    match value {
        "true" | "1" => Ok(true),
        "false" | "0" => Ok(false),
        _ => Err(format!("invalid boolean {value:?}").into()),
    }
}
