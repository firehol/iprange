//! Exact reservation and prepared-output custody checks.

use std::fs::File;

use crate::{error, file_io};

use super::super::namespace::{regular_identity, Identity, Name, NamespaceError};
use super::super::output::PreparedOutput;
use super::super::reservation::{self, Header, Selected};
use super::{Error, FILE_SIZE};

#[derive(Clone, Copy)]
pub(super) enum ReservationLocation {
    Private,
    Canonical,
}

#[derive(Clone, Copy)]
pub(super) enum OutputLocation {
    Private,
    Main,
}

pub(super) struct Expected<'a> {
    pub(super) identity: Identity,
    pub(super) private_name: &'a Name,
    pub(super) header: Header,
    pub(super) block: usize,
    pub(super) reservation_location: ReservationLocation,
    pub(super) output_location: OutputLocation,
}

pub(super) fn verify(
    file: &File,
    output: &PreparedOutput,
    expected: Expected<'_>,
) -> Result<(), Error> {
    verify_inode(file, output, &expected)?;
    verify_location(output, &expected)?;
    verify_contents(file, expected.header, expected.block)
}

fn verify_inode(
    file: &File,
    output: &PreparedOutput,
    expected: &Expected<'_>,
) -> Result<(), Error> {
    match expected.output_location {
        OutputLocation::Private => output.verify_private(),
        OutputLocation::Main => output.verify_main(),
    }
    .map_err(Error::Output)?;
    let destination = output.attempt.destination();
    let directory = destination.directory();
    directory.verify()?;
    if regular_identity(file, directory.identity())? != expected.identity {
        return Err(NamespaceError::IdentityChanged.into());
    }
    Ok(destination.verify_created(file)?)
}

fn verify_location(output: &PreparedOutput, expected: &Expected<'_>) -> Result<(), Error> {
    let destination = output.attempt.destination();
    let directory = destination.directory();
    let name = match expected.reservation_location {
        ReservationLocation::Private => expected.private_name,
        ReservationLocation::Canonical => destination.coordination(),
    };
    directory.verify_name(name, expected.identity)?;
    if matches!(
        expected.reservation_location,
        ReservationLocation::Canonical
    ) {
        directory.require_absent(expected.private_name)?;
    }
    Ok(())
}

fn verify_contents(file: &File, header: Header, block: usize) -> Result<(), Error> {
    if file.metadata().map_err(error::Error::from)?.len() != FILE_SIZE as u64 {
        return Err(Error::LengthChanged);
    }
    select_exact(file, header, block)
}

pub(super) fn select_exact(file: &File, header: Header, block: usize) -> Result<(), Error> {
    let mut bytes = [0; FILE_SIZE];
    file_io::read_exact_at(file, &mut bytes, 0)?;
    if reservation::select(&bytes).map_err(Error::Codec)? != (Selected { header, block }) {
        return Err(Error::HeaderChanged);
    }
    Ok(())
}
